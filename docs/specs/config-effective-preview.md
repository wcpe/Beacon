# 功能规格：配置中心有效预览端点 + 双视图重构

> 状态：已交付@v0.3.0　·　关联 PRD：FR-22（增强 FR-1/FR-6）　·　分支：feature/config-effective-preview

## 1. 背景与目标

运维要"100 台服务器共用一套配置 + 某几台/某区做增量(改/加键)、减量(删键)"。这套能力**已由既有 scope 覆盖链 + DeepMerge 原生支持**（global 基线 ← group ← zone ← server，高层深合并覆盖低层，`key: null` 删键）。问题在于现配置页是扁平 `config_item` 列表，看不到"一套配置文件的覆盖链全貌"，也看不到"某台服务器最终生效的是什么、每个值来自哪一层"。

本功能补齐这两点：① 一个 admin 只读"有效配置预览"端点（含逐键来源 provenance）；② 配置页重构为「服务器视角」+「文件覆盖矩阵」双视图。属第二期治理增强、落在既有覆盖链上，不引入灰度/流量调度等 P2/P3。

## 2. 需求（要什么）

- **后端（里程碑①）**：新增只读 `GET /admin/v1/configs/effective?namespace=&serverId=`（可选 `&group=&zone=` 预览未注册/假定指派目标），复用 `EffectiveService.Resolve` 的合并逻辑，返回某目标合并后的有效配置 + **逐键来源层 provenance** + 被减量删除的键。不挂长轮询、不强制 `RequireRegistered`。
- **前端（里程碑②）**：
  - 服务器视角：选 serverId → 看其有效配置，每键标来源层色块（global/group/zone/server），就地做增量（改/加键）/减量（写 null），保存落到该机 `server` 层。
  - 文件覆盖矩阵：以 dataId 为一等公民，进入后用「层 × 目标」矩阵看全网覆盖链与谁偏离基线；矩阵里点某 server 跳到其有效配置面板。
  - 两视图共用同一 effective 端点与同一来源色块组件，按已落地的 shadcn neutral 主题实现。
- 范围内：上述端点 + 双视图 + 配套 api 客户端/类型。
- 不做（范围外）：不改 `DeepMerge`/`MergeDataID` 等 agent 热路径合并语义；不引入灰度/canary/流量调度/版本编排（P2/P3）；不为其预留字段；不加"原子 promote/demote 跨层迁移"等需新增事务写端点的能力（评审中的 layered-diff 方案，未采纳）。

## 3. 设计（怎么做）

- 决策与防漂移见 **[ADR-0013](../adr/0013-admin-effective-config-preview-and-provenance.md)**：provenance 用**平行的纯函数** `merge.MergeDataIDWithProvenance` 实现，**不改** `DeepMerge`/`MergeDataID`；以"合并结果与 `MergeDataID` 逐一致"的交叉测试守护，防两份实现漂移。
- 后端分层不变：`router → handler(ConfigHandler.Effective) → service(EffectiveService.ResolveWithProvenance) → repository(FindEffectiveCandidates 复用) + merge(纯函数)`。handler 不碰 GORM/合并细节。
- `ResolveWithProvenance(ns, serverId, groupHint, zoneHint)`：serverId 非空时按 `zone_assignment` 解出 (group,zone)，未指派用传入 group/zone；按 dataId 分桶、各层低→高调 provenance 合并；md5 与既有 `Resolve` 一致。
- provenance 数据形：每键 `{path:[]string, scope}`；减量键单列（仅最终态确实不存在的才算）。
- 前端：`api/client.ts` 加 `effectiveConfig()`；新增服务器视角页与文件矩阵视图；来源色块统一组件。

## 4. 任务拆分

- [ ] `merge.MergeDataIDWithProvenance` + 穷举单测 + 与 `MergeDataID` 一致性交叉测试
- [ ] `EffectiveService.ResolveWithProvenance` + 类型
- [ ] `ConfigHandler.Effective` + 视图；路由 `GET /admin/v1/configs/effective`；main.go 调序传 effSvc；修 router 集成测试调用点
- [ ] `go test ./...` / `go vet` / `go build` 绿；加 ResolveWithProvenance 集成测试（integration tag）
- [ ] 前端 api + 服务器视角 + 文件矩阵双视图；`pnpm build` 绿 + 截图验证
- [ ] 文档同步：PRD 状态、ARCHITECTURE、API、CHANGELOG

## 5. 验收标准

- `GET /admin/v1/configs/effective?namespace=prod&serverId=X` 返回与 agent 端 `/beacon/v1/agent/config/effective` **相同的合并内容与 md5**，并额外给出每个键的来源层与被减量删除的键。
- 设 global 基线 + 某 server 层增量(改 a、加 b) + 减量(写 c:null)，预览结果：a 来源 server、b 来源 server、c 出现在"已删除键"且来源 server、其余键来源 global；md5 = 同输入 `Resolve` 的 md5。
- `merge.MergeDataIDWithProvenance` 的合并文本对任意分层输入恒等于 `MergeDataID`（交叉测试覆盖 yaml/json/properties、嵌套、list 整替、null 删键、删后重加）。
- 前端服务器视角能选 serverId 看有效配置+来源色块、就地增量/减量并保存到 server 层；文件矩阵能看覆盖链全貌并跳转；`pnpm build` 绿、截图确认。
- 受影响组件全绿：`go test ./...`（+ integration tag 跑 DSN 集成）、`web build`。

## 6. 风险 / 待定

- **合并双实现漂移**：由 ADR-0013 的一致性交叉测试兜底；若日后要消除双实现，需谨慎重构 agent 热路径并扩测试。
- **provenance 路径与含点键**：内部用不可见分隔符建路径、输出转 `.`，properties 扁平含点键不误拆（已在测试覆盖）。
- **scopeTarget 自由文本**：后端不强约束层↔target 对应，前端录入侧自行按层校验（global 空/group=大区码/zone=zone 码/server=serverId）。
- 红线：端点/前端不得出现灰度/流量调度/版本编排，也不预留字段。
