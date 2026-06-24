# 功能规格：配置/文件发布影响面预览

> 状态：开发中　·　关联 PRD：FR-79　·　分支：feature/fr-79-publish-impact

## 1. 背景与目标
配置中心按 scope 覆盖链（global / group / zone / server）发布；运维点「保存/发布」时，**看不到这次改动会落到哪些正在线上跑的子服**——要心算「这条 config 在哪层、覆盖谁、谁此刻在线」，体验差、易误发到比预期更大的范围。

本需求（P2，增强有效配置预览 FR-22）在发布确认环节补一条只读事实：**「将影响 N 台在线服：[serverId…]」**。后端按 `zone_assignment`（DB 权威归属，ADR-0004）解出某条 scope 覆盖到的子服集合，与内存注册表（注册/健康真源）当前可用实例求交，得到「此刻真正会收到这次变更的在线子服」。

## 2. 需求（要什么）
- 新增只读端点 `GET /admin/v1/configs/impact`，按 scope 算受影响的在线子服集合：
  - 入参：`namespace`、`scopeLevel`、`group`、`scopeTarget`。
  - 出参：`{ namespace, scopeLevel, group, scopeTarget, affected: [serverId…], total }`，`affected` 按 serverId 字典序、`total = len(affected)`。
- 各 scope 的覆盖语义（与 `FindEffectiveCandidates` 覆盖链对称）：
  - `global`：该 namespace 下全部可用实例。
  - `group`：可用实例中解析大区 == `group` 的子服（此层 `group` 即覆盖目标，`scopeTarget` 不参与）。
  - `zone`：可用实例中解析大区 == `group` 且解析小区 == `scopeTarget` 的子服。
  - `server`：可用实例中 `serverId == scopeTarget` 的那一台（在线才计入）。
- 子服的 (group, zone) 归属由 `zone_assignment`（DB）解析；未指派的实例回退 group=GroupHint、zone 为空（与 `EffectiveService.Resolve` 同口径）。
- 「在线 / 可用」口径 = 注册表可用集合（online + degraded），与发现 / 拓扑同口径——degraded 仍持长轮询、仍会收到本次变更，理应计入「将影响」。
- 前端：发布确认对话框（`ConfigSaveConfirmDialog`，FR-67 流）拉该端点，展示「将影响 N 台在线服：serverId…」。
- 范围内：新 `ImpactService` + `ConfigHandler.Impact` + router 加 1 条 impact 路由；`ConfigSaveConfirmDialog` 展示；`web/src/api/client.ts` + `web/src/api/types.ts` 加客户端与类型；`docs/API.md`、`CHANGELOG.md`。
- 不做（范围外）：
  - 不落 DB、不新增表、不改注册/心跳/发布路径。
  - 不算「内容是否真变（md5 diff）」——只算 scope 覆盖面；是否触发热更由既有长轮询比对决定。
  - 文件树（通道 B）发布的影响面预览不在本 FR 内（FR-79 文案含「文件」，但 MVP 先覆盖配置发布确认这一处；文件树同形预览留待需要时另起，不提前镀金）。
  - 不做权限/灰度 cohort 维度的细分（cohort 是 FR-9 的事）。

## 3. 设计（怎么做）
### 3.1 控制面（Go）
- `internal/service/impact_service.go` 新增 `ImpactService`：
  - 依赖 `*runtime.Registry`（在线真源）+ `*repository.ZoneAssignmentRepository`（归属真源）。
  - `Resolve(ns, scopeLevel, group, scopeTarget) (Impact, error)`：
    1. 一次性 `assignRepo.List(ns, "", "")` 拉该环境全部归属，建 `serverId → (group,zone)` 映射（避免逐实例查库的 N+1）。
    2. `registry.List(Filter{Namespace: ns})` 取快照，过滤出可用集合（online+degraded）。
    3. 对每个可用实例解析其 (group,zone)：命中映射用 DB 值，否则回退 (GroupHint, "")。
    4. 按 scopeLevel 用纯函数 `scopeCovers` 判定是否覆盖该实例，命中即收集 serverId。
    5. serverId 去重排序，返回 `Impact{Affected, Total}`。
  - `scopeCovers(level, group, scopeTarget, instGroup, instZone, instServerID)` 为纯函数，集中四层覆盖判定（消灭散落 if，便于穷举单测）。
- `internal/handler/config_handler.go`：
  - `ConfigHandler` 增 `impactSvc *service.ImpactService` 字段；`NewConfigHandler` 增该入参。
  - 新增 `Impact(w, r)`：解析 query，校验 `namespace` 与 `scopeLevel` 非空且 scopeLevel 合法（`model.IsValidScopeLevel`），zone/server 层要求对应定位参数非空（group+scopeTarget / scopeTarget），缺失返回 400；调 service，渲染 `{namespace, scopeLevel, group, scopeTarget, affected, total}`。handler 不碰 GORM / 注册表内部结构（只经 service）。
- `internal/server/router.go`：admin 组加 `r.Get("/configs/impact", h.Config.Impact)`，置于 `{id}` 通配前（与 effective / gray / batch 同理，静态路由优先）。
- 装配：`cmd/beacon/main.go` 构造 `ImpactService`（registry + assignRepo 已就绪）并传入 `NewConfigHandler`。
- 可移植：仅用 `assignRepo.List`（既有 GORM 查询，无方言），无新增 SQL。

### 3.2 前端（React/TS）
- `web/src/api/types.ts`：新增 `ImpactView { namespace; scopeLevel; group; scopeTarget; affected: string[]; total: number }`。
- `web/src/api/client.ts`：新增 `impactPreview(params: { namespace; scopeLevel; group?; scopeTarget? })` → `GET /configs/impact`，复用 `qs`。
- `web/src/pages/configs/ConfigSaveConfirmDialog.tsx`：新增可选 props `namespace/scopeLevel/group/scopeTarget`，对话框打开时拉 impact，展示「将影响 N 台在线服：serverId…」一行（加载中 / 0 台 / N 台三态，纯展示，不阻断发布）。
- `web/src/pages/ConfigsPage.tsx`：把 `activeTab` 的 `scopeLevel/scopeTarget` 透传给对话框（namespace/group 已传）。

## 4. 任务拆分
- [x] service `scopeCovers` 纯函数 + `ImpactService.Resolve` + 单测（四层覆盖、未指派回退、可用集合过滤、server 离线不计、去重排序）
- [x] handler `Impact` + 参数校验 + 装配（main.go / NewConfigHandler）
- [x] router 加 impact 路由
- [x] 前端 types/client/ConfigSaveConfirmDialog/ConfigsPage + vitest
- [x] 文档同步：PRD 状态、docs/API.md、CHANGELOG

## 5. 验收标准
- `go build ./... && go test ./...` 绿，含 `ImpactService` 单测（覆盖 global/group/zone/server 四层覆盖、未指派回退 GroupHint、degraded 计入、lost/offline 不计、server 层目标离线返回空集、affected 去重字典序）。
- 真 MySQL 集成（`go test -tags=integration ./internal/server/...`）含 impact 端点用例：发布前查 impact 与预期受影响集合一致。
- 前端 `pnpm test` + `pnpm build` 绿，含对话框展示 impact 行的用例。
- 发布确认对话框真机显示「将影响 N 台在线服：serverId…」（待真机浏览器验）。

## 6. 风险 / 待定
- 「在线」取可用集合（online+degraded）而非严格 online：与发现/拓扑/长轮询口径一致——degraded 仍会收到变更。文案用「在线服」，把 degraded 也算入「会收到」，避免漏报。
- 影响面为读时刻快照：与真正发布之间若有实例上下线，受影响集可能瞬时出入。可接受——只读预览、不参与发布决策。
- 用 DB（`zone_assignment`）而非注册表 `ResolvedGroup/Zone` 解析归属：两者在线时一致，但 DB 是 zone 归属的唯一权威（ADR-0004），预览以权威为准，口径与 agent `Resolve` 一致。
