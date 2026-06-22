# 功能规格：文件树有效预览 + 逐文件/逐键来源 provenance

> 状态：开发中　·　关联 PRD：FR-45　·　分支：feature/fr-45-file-tree-effective-preview

## 1. 背景与目标

FR-22（[ADR-0013](../adr/0013-admin-effective-config-preview-and-provenance.md)）已给配置中心（通道A）做了 admin 只读有效配置预览：`GET /admin/v1/configs/effective` 返回某目标合并后的有效配置 + **逐叶子键来源层**（global/大区/小区/单服）+ 被 `null` 减量删除的键。其 provenance 经 `merge.MergeDataIDWithProvenance` 平行纯函数算出，以「合并文本与 `MergeDataID` 逐一致」交叉测试守护、绝不改 agent 热路径。

FR-44（[ADR-0029](../adr/0029-file-tree-structured-deep-merge.md)）把通道A 的键级深合并嫁接到了通道B（文件树托管）：结构化文件（`.yml`/`.yaml`/`.json`/`.properties`）跨四层按键深合并，非结构化整文件覆盖兜底，按文件 `WholeFileOverride` 可豁免。但通道B 此前**只有 agent 侧 `files/manifest`/`files/content` 出口**，admin 侧没有「给定某服看每个文件最终合并成什么、每个键来自哪层」的入口——这正是 FR-46 审核台 diff「期望合并值」一侧的数据源缺口。

目标：把 FR-22 的预览 + provenance 能力**扩展到文件树**——admin 只读预览某 `(namespace, serverId)` 的有效文件树，每个文件给出合并结果 + 逐键来源（结构化文件）或整文件来源层（非结构化/豁免文件）+ 是否 `wholeFileOverride` 豁免。属 P2 治理增强，依赖 FR-44。

## 2. 需求（要什么）

- 范围内：
  - **后端只读端点** `GET /admin/v1/files/effective?namespace=&serverId=&group=&zone=`（命名/形参与 FR-22 的 `configs/effective` 对齐）：解析某目标的有效文件树，逐文件返回 `path` / 合并后 `content` / 合并后 `md5` / `wholeFileOverride` / 合并模式 / `sources` / `deletions`。
  - **结构化文件**（非豁免、后缀结构化、内容可解析）：`sources` 为逐叶子键最终来源层，`deletions` 为被高层 `null` 减量删除且最终不存在的键——复用 `merge.MergeDataIDWithProvenance`（与通道A 同一套）。
  - **非结构化 / 豁免 / 坏内容**文件：整文件覆盖，`sources` 为单条空路径来源（= winner 层 scope），`deletions` 空；`wholeFileOverride` 标记如实回吐。
  - **不挂长轮询、不强制注册**（同 FR-22 的克制）：可预览未注册或假定指派的目标。
  - **前端只读「文件树有效预览」视图**（某服视角：选目标 → 列文件 → 看合并结果 + 逐键来源徽标 + 豁免/被删键标注），复用既有 React/shadcn 栈与 FR-22 预览页模式，作为后续 FR-46 审核台 diff 一侧数据源。
- 不做（范围外）：
  - **不改 FR-44 的 `filetree.Resolve` / `merge` 热路径**；provenance 另走平行纯函数（同 ADR-0013 思路），以「与 `Resolve`/下发结果逐一致」交叉测试防双实现漂移。
  - 不做 diff / 拓印回写 / 单人自审门（属 FR-46）。
  - 不引入按 path 的字段级 schema 注册表（合并规则与通道A 一致、保守）。
  - 不做文件树托管的写/编辑 UI（通道B admin 写路径已存在；本 FR 只做只读预览视图）。

## 3. 设计（怎么做）

**沿用 [ADR-0013](../adr/0013-admin-effective-config-preview-and-provenance.md) 的 provenance 模式，无需新 ADR**（把同一「平行纯函数 + 交叉一致测试」做法套到 ADR-0029 的文件树合并模型上）。

- **`internal/filetree`**：新增纯函数 `ResolveWithProvenance(candidates []model.FileObject) []EffectiveFileProvenance`，与 `Resolve` **同一套 per-path 分桶 + winner + 分流判定**，但额外产出来源：
  - 结构化且非豁免：按 scope 低→高组 `merge.ProvLayer`，调 `merge.MergeDataIDWithProvenance` 得 `content`/`sources`/`deletions`；`md5 = md5(content)`。
  - 非结构化 / `WholeFileOverride` / 结构化坏内容：整文件取 winner，`content = winner.Content`、`md5 = winner.ContentMD5`、`sources = [{path:[], scope:winnerScope}]`、`deletions=nil`。
  - 每条带 `WholeFile bool`（是否整文件模式）+ `Path` + `MD5` + `Content`。
  - 结果按 `path` 字典序稳定排序（同 `Resolve`）。
  - **与 `Resolve` 逐一致**：对任意候选集，`ResolveWithProvenance` 每个 path 的 `Content`/`MD5` 必须等于 `Resolve` 同 path 的 `Content`/`MD5`（交叉测试硬约束，防两份解析实现漂移）。
- **`internal/service`**：`FileEffectiveService` 新增 `ResolveWithProvenance(ns, serverID, groupHint, zoneHint) (ProvenancedFileTree, error)`：`serverID` 非空时优先按 `zone_assignment` 解出 `(group,zone)`，否则用传入 `groupHint`/`zoneHint`；拉四层候选 → `filetree.ResolveWithProvenance`。不碰 hub（不挂长轮询）。
- **`internal/handler`**：`FileHandler` 已持有 `effSvc *FileEffectiveService`，新增 `Effective` handler 处理 `GET /admin/v1/files/effective`，参数校验同 `configs/effective`（`namespace` 必填、`serverId`/`group` 至少一个），组装对外视图。
- **`internal/server/router.go`**：admin 组内文件树段加一条只读 `r.Get("/files/effective", h.File.Effective)`（静态路由置于 `{id}` 前）。
- **`cmd/beacon/main.go`**：无需新依赖装配（`fileEffectiveService` 与 `fileHandler` 已存在）。
- **前端**：新增 `fileEffective()` api 客户端 + 类型；新增只读页 `FileEffectivePreviewPage`（`/file-preview`，侧栏加导航项）+ 展示组件——选目标（实例下拉）→ 列文件 → 每文件展示合并 content、来源徽标（结构化逐键 / 整文件单层）、豁免标记与被删键。vitest 锁定渲染契约。

**确定性**：provenance 版与 `Resolve` 共用 `merge` 的固定键序序列化与同一分流判定（winner 层 `WholeFileOverride` + 后缀，与遍历顺序无关），相同候选恒得相同 content/md5/来源。

## 4. 任务拆分

- [ ] 规格（本文）+ PRD FR-45 状态「计划」→「开发中」。
- [ ] `internal/filetree`：`ResolveWithProvenance` + `EffectiveFileProvenance`（穷举单测先行：逐键来源 / null 删键 / 非结构化整文件 / 豁免 / 坏内容回退 / 与 `Resolve` 交叉一致）。
- [ ] `internal/service`：`FileEffectiveService.ResolveWithProvenance` + `ProvenancedFileTree`（集成测试：指派解析、与 `Resolve` 内容/md5 一致、来源正确）。
- [ ] `internal/handler`：`FileHandler.Effective` + 视图 + 参数校验单测。
- [ ] `internal/server/router.go`：只读路由。
- [ ] 前端：api 客户端 + 类型 + 只读预览页 + 组件 + vitest + mock handler。
- [ ] 文档同步：ARCHITECTURE（§5.1 或 §4 接口）、API（新端点）、CHANGELOG 未发布段末尾追加一行。

## 5. 验收标准

- `GET /admin/v1/files/effective?namespace=&serverId=`：同 path `app.yml` global `{a:1,b:{x:1}}` + zone `{b:{y:2}}` + server `{a:null,c:3}` → 该文件 `content` 合并为 `{b:{x:1,y:2},c:3}`，`sources` 含 `b.x`←global、`b.y`←zone、`c`←server，`deletions` 含 `a`←server，`wholeFileOverride=false`、合并模式=deep-merge。
- 标 `wholeFileOverride=true` 的结构化文件：`content` 逐字节等于 winner 原文、`sources` 为单条空路径=winner 层、`deletions` 空、`wholeFileOverride=true`。
- 非结构化文件（`.allin`/`.js`）：整文件取最高层、`sources` 单条空路径=winner 层。
- 坏结构化内容：回退整文件取 winner，`sources` 单条空路径=winner 层（不 panic）。
- **与下发逐一致**：对任意候选集，`ResolveWithProvenance` 每个 path 的 `content`/`md5` == `Resolve`（即 agent 经 `files/content` 拿到的）——交叉测试穷举覆盖结构化/非结构化/豁免/坏内容/混合层。
- 端点不挂长轮询、不强制注册（未注册目标也能预览）。
- 前端只读视图：选目标后列出文件、展示合并 content 与来源徽标、豁免与被删键标注；vitest 绿。
- 受影响组件测试全绿（`go test ./...` + `cd web && pnpm build && pnpm test`）。

## 6. 风险 / 待定

- **双实现漂移**：provenance 版与 `Resolve` 是两份遍历——靠「内容/md5 逐一致」交叉测试钉死（同 FR-22 对 `MergeDataID` 的做法），任一分支改动致结果分叉即测试红。
- **整文件文件的「逐键来源」语义**：非结构化/豁免文件无键级来源，统一表达为「单条空路径来源 = winner 层」（前端渲染为「整文件来自 X 层」徽标），不强造假的逐键拆分。
- **前端范围**：通道B 此前无 admin 文件树页（`/files` 重定向到 `/configs`），故本 FR 新建独立只读预览页而非改既有页；写/编辑 UI 仍不在范围（FR-46 再做审核台）。
