# 功能规格：环境（namespace）增删改查补全

> 状态：开发中　·　关联 PRD：FR-53（增强 FR-6）　·　分支：feature/fr-53-namespace-crud
>
> **后续演进（2026-07-29）**：本文记录早期 CRUD 设计；旧 `/admin/v1/namespaces/{code}` 与历史 `/admin/v2/namespaces/{id}` DELETE 现统一迁移为 `410 namespace_delete_migrated`，不再提供硬删旁路。后续清理能力改由 FR-216/218 的归档、审批与墓碑契约承接。

## 1. 背景与目标

环境（namespace）此前只有「列表 / 新建」（FR-7/FR-30 已落 `namespace.create` 审计），缺 Update 与 Delete。
运维改环境显示名、清理误建 / 废弃环境只能直接动库，既不安全也无审计。本期补齐 CRUD：
显示名随时可改、删除带前置守卫（有在用数据则禁删并明确提示原因），全部入审计。属 P2、增强 FR-6，
不引入新决策、不需新 ADR（既有 CRUD 补全）。

## 2. 需求（要什么）

- **Update**：环境显示名 `name` 随时可改（`code` 为不可变身份键，不改）。
- **Delete 迁移**：旧删除入口统一返回 `410 namespace_delete_migrated`；不再检查历史删除守卫、不硬删、不写 `namespace.delete` 审计、不隐式创建审批请求。
- 操作入审计：保留 `namespace.update`；旧 Delete 迁移响应本身不产生领域审计。
  operator 由认证态派生、`targetType=namespace`、`targetRef=code`，detail 不含敏感数据。
- 前端：在既有「环境管理」页（`/namespaces`）上扩展改名 / 删除 + 完整管理 UI（列表 / 新建 / 改名 / 删除）。
- 范围内：namespace 的 Update（仅 name）+ 审计 + 前端管理页；Delete 已迁移为 410。
- 不做（范围外）：改 `code`（身份键不可变）；级联删除环境下的配置 / zone / 实例（守卫禁删而非级联，
  避免误删在用数据）；多人审批 / 软删恢复 namespace（namespace 表无软删需求，见 §3）。

## 3. 设计（怎么做）

### 数据模型
`namespace` 表结构不变（`code` 唯一 + `name` + 时间戳，**无 `deleted_at`**）。
早期硬删设计已废弃；当前旧 DELETE 只返回迁移错误，不改变表结构。namespace 生命周期的归档、恢复与墓碑列形态由 FR-216/218 后续规格承接。

### 控制面（分层 router→handler→service→repository）
- `internal/model/enums.go`：保留 `ActionNamespaceUpdate = "namespace.update"`；旧 Delete 迁移后不再新增 `namespace.delete` 写路径。
- `internal/apperr/apperr.go`：新增 `ErrNamespaceNotFound`（404）、`ErrNamespaceHasInstances` /
  `ErrNamespaceHasAssignments` / `ErrNamespaceHasConfigs` / `ErrNamespaceHasFiles` /
  `ErrNamespaceHasOverrideSets`（均 409 Conflict，各类删除守卫拒因）。
- `internal/repository/namespace_repo.go`：仅保留 `UpdateName`（按 code 改 name）；Delete 路径已移出本规格，不再驱动 repository 硬删。
- `internal/service/namespace_service.go`：`NamespaceService` 注入装配所需依赖。
  - `Update(code, name, operator, clientIP)`：环境不存在 → `ErrNamespaceNotFound`；事务内改名 + 写 `namespace.update` 审计原子完成。
- `internal/handler/namespace_handler.go`：`Delete` 已迁移为 410 响应，不再调用 service。
- `internal/server/router.go`：在既有 `GET/POST /admin/v1/namespaces` 后挂写方法时，`DELETE` 现统一回迁移错误；readonly 角色仍先经 `readonlyWriteGuard` 403。
- 装配（`cmd/beacon/main.go`）：`NamespaceService` 构造移到 registry / assignRepo / configRepo 就绪之后
  （或经 setter 注入），保持手工注入不引 DI。

### 前端（React + shadcn-ui）
- `web/src/api/client.ts`：新增 `updateNamespace(code, name)`、`deleteNamespace(code)`。
- `web/src/pages/NamespacesPage.tsx`：在既有「列表 + 新建」上扩展——
  每行加「改名」（Dialog 改 name）与「删除」（AlertDialog 二次确认，沿 ApiKeysPage 模式）操作列；
  守卫拒删的后端中文错误经 `useMessage.showError` 提示。
- 审计页（`AuditsPage`）直接展示 `action` 原文（现状无中文映射），故 `namespace.update/delete` 无需额外前端映射。

## 4. 任务拆分
- [ ] 规格（本文）
- [ ] Go 测试先行：`namespace_service_test.go` 覆盖 Update（成功 / 不存在）；删除迁移测试覆盖 V1/V2 410 与 readonly 403
- [ ] enums / apperr 常量 + repository 改名方法；删除迁移错误码 `namespace_delete_migrated`
- [ ] service Update + 装配接线；Delete 不再进入 service 硬删
- [ ] handler + router 端点
- [ ] 前端 client + 管理页改名 / 删除 + vitest
- [ ] 文档同步：PRD 状态、API.md、CHANGELOG（未发布段末尾追加一行）

## 5. 验收标准
- Go：`go build ./...` / `go vet ./...` / `go test ./...` 全绿；删除迁移单测覆盖 full 角色 410、readonly 403 与不产生领域副作用。
- 改名成功落库且产一条 `namespace.update` 审计；旧删除入口返回 `410 namespace_delete_migrated`，不硬删、不写 `namespace.delete` 审计。
- 前端：`pnpm test` + `pnpm build` 全绿；管理页可改名，删除能力改由后续归档 / 审批 / 墓碑流程承接。
- 不破坏既有 namespace 列表 / 新建行为与序列化格式（`{code, name}` 不变）。

## 6. 风险 / 待定
- **删除守卫范围（历史）**：早期硬删设计已被废弃。现阶段旧 DELETE 只返回迁移错误，后续真实清理由 FR-216/218 在归档、审批与墓碑契约中重新定义影响预览和执行守卫。
- 硬删 vs 软删：早期直接硬删已废弃；namespace 生命周期改走归档 / 恢复 / 墓碑。
