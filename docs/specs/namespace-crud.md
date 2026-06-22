# 功能规格：环境（namespace）增删改查补全

> 状态：开发中　·　关联 PRD：FR-53（增强 FR-6）　·　分支：feature/fr-53-namespace-crud

## 1. 背景与目标

环境（namespace）此前只有「列表 / 新建」（FR-7/FR-30 已落 `namespace.create` 审计），缺 Update 与 Delete。
运维改环境显示名、清理误建 / 废弃环境只能直接动库，既不安全也无审计。本期补齐 CRUD：
显示名随时可改、删除带前置守卫（有在用数据则禁删并明确提示原因），全部入审计。属 P2、增强 FR-6，
不引入新决策、不需新 ADR（既有 CRUD 补全）。

## 2. 需求（要什么）

- **Update**：环境显示名 `name` 随时可改（`code` 为不可变身份键，不改）。
- **Delete 守卫**：该环境下满足以下任一前置条件即**禁删并明确提示**（各类区分给清晰错误码）：
  1. 有**已注册实例**（内存注册表中该 namespace 下有实例条目）→ `NAMESPACE_HAS_INSTANCES`
  2. 有**已指派 zone**（`zone_assignment` 该 namespace 下有未软删指派）→ `NAMESPACE_HAS_ASSIGNMENTS`
  3. 有**已有配置**（`config_item` 该 namespace 下有未软删配置项）→ `NAMESPACE_HAS_CONFIGS`
  4. 有**文件树**（`file_object` 该 namespace 下有未软删文件，通道B）→ `NAMESPACE_HAS_FILES`
  5. 有**覆盖集**（`file_override_set` 该 namespace 下有未软删覆盖集，FR-15）→ `NAMESPACE_HAS_OVERRIDE_SETS`
- **可删放行**：以上前置条件均不满足 → 硬删该环境行，记 `namespace.delete` 审计。
- 操作入审计：新增审计动作 `namespace.update` / `namespace.delete`，沿用既有 `audit_log`，
  operator 由认证态派生、`targetType=namespace`、`targetRef=code`，detail 不含敏感数据。
- 前端：在既有「环境管理」页（`/namespaces`）上扩展改名 / 删除 + 完整管理 UI（列表 / 新建 / 改名 / 删除）。
- 范围内：namespace 的 Update（仅 name）/ Delete（带守卫）+ 审计 + 前端管理页。
- 不做（范围外）：改 `code`（身份键不可变）；级联删除环境下的配置 / zone / 实例（守卫禁删而非级联，
  避免误删在用数据）；多人审批 / 软删恢复 namespace（namespace 表无软删需求，见 §3）。

## 3. 设计（怎么做）

### 数据模型
`namespace` 表结构不变（`code` 唯一 + `name` + 时间戳，**无 `deleted_at`**）。
删除采用**硬删**：与 `config_item` / `zone_assignment` 的软删（[ADR-0008](../adr/0008-config-soft-delete-and-effective-md5.md)）
不同——软删是为「同标识软删后可重建且唯一键仍生效」服务的；namespace 删除仅在「环境内已无任何在用数据」
时才放行，删后不存在「同 code 重建要避开软删行」的诉求，故沿用既有 namespace 表设计直接硬删，不引入软删列。

### 控制面（分层 router→handler→service→repository）
- `internal/model/enums.go`：新增 `ActionNamespaceUpdate = "namespace.update"`、`ActionNamespaceDelete = "namespace.delete"`。
- `internal/apperr/apperr.go`：新增 `ErrNamespaceNotFound`（404）、`ErrNamespaceHasInstances` /
  `ErrNamespaceHasAssignments` / `ErrNamespaceHasConfigs` / `ErrNamespaceHasFiles` /
  `ErrNamespaceHasOverrideSets`（均 409 Conflict，各类删除守卫拒因）。
- `internal/repository/namespace_repo.go`：新增 `UpdateName`（按 code 改 name）、`DeleteByCode`（硬删）。
- `internal/repository/assignment_repo.go`：新增 `CountByNamespace`（数该 namespace 未软删指派）。
- `internal/repository/config_repo.go`：新增 `CountByNamespace`（数该 namespace 未软删配置项）。
- `internal/repository/file_repo.go` / `override_set_repo.go`：各新增 `CountByNamespace`
  （数该 namespace 未软删文件树 / 覆盖集，复用既有 `active()` 软删过滤）。
- `internal/runtime/registry.go`：新增 `CountByNamespace`（数该 namespace 内存实例条目数）。
- `internal/service/namespace_service.go`：`NamespaceService` 注入装配所需依赖
  （namespace repo、assignment repo、config repo、file repo、override-set repo、注册表计数器、审计 repo）。
  - `Update(code, name, operator, clientIP)`：环境不存在 → `ErrNamespaceNotFound`；
    事务内改名 + 写 `namespace.update` 审计原子完成。
  - `Delete(code, operator, clientIP)`：环境不存在 → `ErrNamespaceNotFound`；
    依次查守卫（实例 / zone / 配置 / 文件树 / 覆盖集），命中即返对应 409 错误、**不删不审计**；
    全过则事务内硬删 + 写 `namespace.delete` 审计。
  - 注册表计数经服务内最小接口 `instanceCounter` 注入（不直依赖 `*runtime.Registry` 具体类型，便于单测）。
- `internal/handler/namespace_handler.go`：新增 `Update`（`PUT /admin/v1/namespaces/{code}`）、
  `Delete`（`DELETE /admin/v1/namespaces/{code}`），仅做编解码 + service 调用。
- `internal/server/router.go`：在既有 `GET/POST /admin/v1/namespaces` 后挂
  `PUT/DELETE /admin/v1/namespaces/{code}`（写方法，readonly 角色经 `readonlyWriteGuard` 403）。
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
- [ ] Go 测试先行：`namespace_service_test.go` 覆盖 Update（成功 / 不存在）+ Delete 守卫四态
      （有实例拒 / 有 zone 拒 / 有配置拒 / 全空放行）+ 审计落库与「拒删不留审计」
- [ ] enums / apperr 常量 + repository 计数与改删方法
- [ ] service Update / Delete + 装配接线
- [ ] handler + router 端点
- [ ] 前端 client + 管理页改名 / 删除 + vitest
- [ ] 文档同步：PRD 状态、API.md、CHANGELOG（未发布段末尾追加一行）

## 5. 验收标准
- Go：`go build ./...` / `go vet ./...` / `go test ./...` 全绿；新增 service 单测覆盖删除守卫三类拒绝 + 放行。
- 改名成功落库且产一条 `namespace.update` 审计；删除成功硬删且产一条 `namespace.delete` 审计；
  三类守卫命中时禁删且**不产审计**。
- 前端：`pnpm test` + `pnpm build` 全绿；管理页可改名 / 删除，守卫错误中文提示可见。
- 不破坏既有 namespace 列表 / 新建行为与序列化格式（`{code, name}` 不变）。

## 6. 风险 / 待定
- **删除守卫范围**：守卫已含**文件树（`file_object`，通道B）与覆盖集（`file_override_set`，FR-15）**，
  故删除前需先清空一切 namespace 维度配置数据（实例 / zone / 配置 / 文件树 / 覆盖集），命中各返专门 409。
  此为忠于 PRD FR-53「已有配置则禁删」意图的完整化——文件树 / 覆盖集同属该环境的「配置数据」，
  且 `file_repo` 等纯按 `namespace_code` 字符串定位：若漏守卫硬删环境行，会留下孤儿文件 / 覆盖集，
  运维以同 code 重建环境后这些孤儿会静默重新归属并经 manifest/effective 下发，属潜在错误下发，故必须纳入守卫。
- 硬删 vs 软删：见 §3，按 namespace 表既有设计硬删。若未来需要 namespace 软删 / 回收站，再行评估。
