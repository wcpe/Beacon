# ADR-0013：admin 有效配置只读预览 + 逐键来源 provenance

**状态**：已接受

## 背景

配置中心的合并能力（scope 覆盖链 DeepMerge）此前只在 agent 侧 `GET /beacon/v1/agent/config/effective`（长轮询）暴露；admin 侧只有按单个 `config_item` 的 CRUD/历史/回滚/diff，**没有"给定目标看合并后有效配置"的入口**。

管理台要做"服务器视角：某台最终生效什么、每个键来自哪一层"和"文件覆盖矩阵：谁偏离基线"，都需要：① admin 能取到合并后有效配置；② 知道**每个键的来源层（provenance）**与被减量（`key: null`）删除的键。FR-1 的 `EffectiveService.Resolve` 已是可复用纯查询，但不产出 provenance。

## 决策

1. **新增只读 admin 端点** `GET /admin/v1/configs/effective?namespace=&serverId=`（可选 `&group=&zone=`）。复用 `EffectiveService` 的解析（`FindEffectiveCandidates` + 按 dataId 分桶合并），**不挂长轮询、不强制 `RequireRegistered`**，可预览未注册或假定指派的目标。与既有只读预览端点 `/admin/v1/override-sets/{id}/dry-run` 同款克制。
2. **provenance 用平行纯函数实现，绝不改动 agent 热路径**。新增 `merge.MergeDataIDWithProvenance`（与 `MergeDataID` 同样的合并规则，额外记录逐叶子键的最终来源层与被 null 删除的键）；**`DeepMerge`/`MergeDataID` 一字不改**（它们是 agent 下发的高风险热路径，受 ADR-0008 与既有穷举单测保护）。
3. **防双实现漂移**：以"`MergeDataIDWithProvenance` 的合并文本对任意分层输入恒等于 `MergeDataID`"的**一致性交叉测试**作为硬约束（覆盖 yaml/json/properties、嵌套深合并、list 整替、null 删键、删后重加、标量↔map 替换、空层、类型不一致）。provenance 元数据另有穷举单测（含**删整子树后高层重加部分子键**时父键不误报减量）。任一分支改动导致两者结果分叉即测试红。
4. **provenance 仅 admin 侧产出**：agent 下发结构（`Effective`/md5）不变，不向数据面泄露额外信息；provenance 只服务于管理台展示。

## 理由

- 复用既有纯查询，零新增合并逻辑、零 repository 改动，端点是薄封装——符合简单优先与范围纪律。
- 不动 `DeepMerge`/`MergeDataID` 避免给 agent 收敛路径引入回归风险；平行函数 + 交叉测试在"不碰热路径"与"避免逻辑漂移"间取得平衡。
- provenance 由服务端权威计算，前端直接用，避免前端再实现一份 DeepMerge 造成口径漂移（这是把"逐键来源/键级三态"做对的前提）。

## 架构边界论证（为何不违"控制面只存事实、禁游戏逻辑"）

该端点是对既有"配置事实 + 覆盖链合并"的**只读查询**，与 agent 端 effective 同源、仅多产出展示用的来源标注，不编排、不决策、不写游戏逻辑。不引入灰度/canary/流量调度/版本编排（仍属 P2/P3，不碰、不预留字段）。

## 后果

- 新增 `merge.MergeDataIDWithProvenance` 与 `merge.KeyProvenance`/`ProvLayer` 类型（纯函数包）。
- `EffectiveService` 增 `ResolveWithProvenance`；`ConfigHandler` 持有 `EffectiveService` 并新增 `Effective` handler；`router.go` 增一条只读路由；`main.go` 装配调序。
- 同步 `docs/API.md`（新端点）与 `CHANGELOG`。

## 备选方案

- **改造 `DeepMerge` 内联记录 provenance**：动 agent 高风险热路径，回归面大。否决，改平行函数 + 交叉测试。
- **前端本地重算合并/来源**：现有 `diff` 端点只回原文，前端要自实现一份 DeepMerge，必然与后端漂移。否决。
- **新增"分层有序覆盖链"聚合端点**：现有 `GET /configs?dataId=` + zone/instance 客户端 join 已够文件矩阵用，额外聚合端点属镀金。否决（除非前端聚合压力被证实过大再议）。
- **layered-diff 的原子 promote/demote 跨层迁移**：需新增事务性写端点、逼近越界。否决，本期不做。
