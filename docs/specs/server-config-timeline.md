# 功能规格：per-server 有效配置变更时间线

> 状态：开发中　·　关联 PRD：FR-80　·　分支：feature/fr-80-config-timeline

## 1. 背景与目标
某子服（serverId）当前生效的配置，是按其覆盖链（global / group / zone / server 四层）合并出来的。运维想回答「这台服的有效配置**何时因哪次发布变过**」时，今天只能：知道该服解析到哪几层、再分别去各 config 项的版本历史（FR-3 `/configs/{id}/revisions`）里逐个翻，自己把多条历史按时间合并——既费力又易漏层。

本需求（P2，feat）在**服务器详情**加一条只读「变更历史」时间线：把这台服覆盖链涉及的全部 config 项的发布记录（`config_revision`）汇总，按时间倒序展示「哪个 dataId 在哪层、第几版、谁在何时发的、md5、说明」。一眼看清「这台服的配置何时被哪次发布动过」。

## 2. 需求（要什么）
- 新增只读端点 `GET /admin/v1/instances/{serverId}/config-timeline?namespace=&group=`：
  - 入参：`serverId`（路径）、`namespace`（必填）、`group`（可选 groupHint，未指派时定位 group 层）。
  - 出参：`{ namespace, serverId, group, zone, items: [ { configItemId, dataId, scopeLevel, scopeTarget, version, md5, operator, comment, createdAt } ... ] }`，`items` 按 `createdAt` 倒序（同刻按 configItemId、version 再排，稳定）。
- 覆盖链解析与有效配置同口径：先按 `zone_assignment`（DB 权威，ADR-0004）解出 (group, zone)，未指派则 group=groupHint、zone 空；再取该链涉及的 config 项集合（= `FindEffectiveCandidates` 的同一四层候选集）。
- 时间线 = 这批 config 项的**全部历史版本**（`config_revision`，含首发 / 发布 / 回滚），按时间倒序汇总。每条标注其所属 config 项的 scope（哪层覆盖）。
- 一次性聚合：按 itemID 集合一次查全部 revision，避免逐项查库的 N+1。
- 前端：`ServerDetailSheet` 加「变更历史」分区，列时间线（dataId@scope、版本、时间、操作者、说明）。
- 范围内：`EffectiveService` 加 `ConfigTimeline` 解析 + revision 仓库加批量按 itemID 查询；`InstanceHandler.ConfigTimeline`；router 加 1 条 `config-timeline` 路由；`ServerDetailSheet` 展示；`web/src/api/client.ts` + `types.ts` 加客户端与类型；`docs/API.md`、`CHANGELOG.md`。
- 不做（范围外）：
  - 不落 DB、不新增表、不改注册/心跳/发布路径。
  - 不算「有效配置合并值在该时刻是什么」——只列触发变更的发布记录（合并值预览是 FR-22）。
  - 不含 content 正文（时间线只给元信息；要看内容走既有 `/configs/{id}/revisions/{version}` 或 diff）。
  - 不含文件树（通道 B）变更——本 FR 聚焦配置中心；文件树同形时间线留待需要时另起，不提前镀金。
  - 不做灰度 cohort 维度（FR-9 的事）。

## 3. 设计（怎么做）
### 3.1 控制面（Go）
- `internal/repository/revision_repo.go`：新增 `ListByItemIDs(itemIDs []uint) ([]ConfigRevision, error)`，一条 `WHERE config_item_id IN (?)` 查全部历史版本（敏感快照走既有解密；不返回 content 由上层不读即可，但仍解密以复用同一路径——实际上层映射只取元信息，不读 content）。空集合直接返回空切片（不发查询）。
- `internal/service/effective_service.go`：新增 `ConfigTimeline(ns, serverID, groupHint string) (ConfigTimeline, error)`：
  1. 复用现有解析：按 `zone_assignment` 解出 (group, zone)，未指派回退 groupHint / 空（与 `Resolve` 同口径）。
  2. `configRepo.FindEffectiveCandidates(ns, group, zone, serverID)` 取该链四层候选 config 项。
  3. 建 `itemID → 候选项（dataId/scopeLevel/scopeTarget）` 映射；`revRepo.ListByItemIDs(itemIDs)` 一次拉全部 revision。
  4. 把每条 revision 关联其 config 项的 scope 元信息，组装 `TimelineEntry`，按 `CreatedAt` 倒序（同刻按 configItemID、version 兜底稳定排序）。
  - 返回结构 `ConfigTimeline{ Namespace, ServerID, Group, Zone, Entries []TimelineEntry }`，`TimelineEntry{ ConfigItemID, DataID, ScopeLevel, ScopeTarget, Version, MD5, Operator, Comment, CreatedAt }`。
  - `EffectiveService` 新增 `revRepo *repository.ConfigRevisionRepository` 字段；`NewEffectiveService` 增该入参（装配处同步）。
- `internal/handler/instance_handler.go`：
  - `InstanceHandler` 增 `effSvc` 窄依赖（仅供 config-timeline，经接口或直接持 `*service.EffectiveService`）。为避免 handler 直碰 GORM/内存：经 service 取数据，handler 只组视图。
  - 新增 `ConfigTimeline(w, r)`：取路径 `serverId`、query `namespace`（空→400）、`group`；调 service，渲染 `{namespace, serverId, group, zone, items:[...]}`。
- `internal/server/router.go`：admin 组加 `r.Get("/instances/{serverId}/config-timeline", h.Instance.ConfigTimeline)`（{serverId} 子路径，不与静态 `/instances/offline` 冲突）。
- 装配：`cmd/beacon/main.go` 把 `effectiveService` 传入 `NewInstanceHandler`。
- 可移植：仅用 `IN (?)` 占位与既有归属/候选查询，无方言。

### 3.2 前端（React/TS）
- `web/src/api/types.ts`：新增 `ConfigTimelineEntry`（含 configItemId/dataId/scopeLevel/scopeTarget/version/md5/operator/comment/createdAt）与 `ConfigTimelineView { namespace; serverId; group; zone; items: ConfigTimelineEntry[] }`。
- `web/src/api/client.ts`：新增 `serverConfigTimeline(params: { serverId; namespace; group? })` → `GET /instances/{serverId}/config-timeline`。
- `web/src/pages/servers/ServerDetailSheet.tsx`：加「变更历史」分区，`useQuery` 拉时间线，列每条（dataId · scope 中文、v{version}、相对/绝对时间、operator、comment）；加载 / 空 / 列表三态。

## 4. 任务拆分
- [ ] repo `ListByItemIDs` + service `ConfigTimeline` + 单测（多层多版本按时间倒序、未指派只含 global 层历史、空候选返回空、按 itemID 一次聚合）
- [ ] handler `ConfigTimeline` + 参数校验 + 装配（main.go / NewInstanceHandler）
- [ ] router 加 config-timeline 路由
- [ ] 前端 types/client/ServerDetailSheet + vitest
- [ ] 文档同步：PRD 状态、docs/API.md、CHANGELOG

## 5. 验收标准
- `go build ./... && go test ./...` 绿，含 service 单测（覆盖：四层均有发布时时间线含全部版本且按时间倒序、未指派 server 只含 global 层历史、无候选返回空、回滚记录也在时间线内）。
- 真 MySQL 集成（`go test -tags=integration ./internal/server/...`）含端点用例：建多层多版本后查 config-timeline 与预期条目/顺序一致。
- 前端 `pnpm test` + `pnpm build` 绿，含 `ServerDetailSheet` 渲染时间线分区的用例。
- 服务器详情真机显示「变更历史」时间线（待真机浏览器验）。

## 6. 风险 / 待定
- 覆盖链按**当前**归属解析：历史上该服曾属别的 zone、那段时间的覆盖层不会回溯还原。可接受——MVP 给「当前覆盖链涉及的 config 项的发布史」，不做归属变迁的时间旅行（避免镀金）。
- 时间线取该服覆盖链上**所有** config 项的全部历史，可能较长：当前不分页（与 `/configs/{id}/revisions` 一致不分页）。若单服覆盖项极多导致过长，再按需加分页（YAGNI，先不做）。
- 用 DB（`zone_assignment`）而非注册表解析归属：与 agent `Resolve`、FR-79 impact 同口径，DB 是 zone 归属唯一权威（ADR-0004）。
