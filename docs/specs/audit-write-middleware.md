# 功能规格：写操作审计中间件兜底

> 状态：开发中　·　关联 PRD：FR-72（增强 FR-7）　·　分支：feature/fr-72-audit-middleware

## 1. 背景与目标

现有审计（FR-7 / FR-30）是**专项审计**：各 service 在写事务内显式落一条领域审计（如 `config.publish`、`zone.assign`、`file.import`），覆盖了当下绝大多数写端点。但这是"逐处补写"模式——新加写端点若忘了补审计，就会出现审计盲区，事后难追责。

目标（属 P2 治理增强）：在 `/admin/v1` 写端点上加一道**兜底审计中间件**，对**尚无专项审计**的写操作（POST/PUT/DELETE）统一补记一条审计（操作者 + 动作 + 目标），让"任何写操作都有迹可循"成为结构性保证而非逐处自觉。既有专项审计**全部保留不动**，中间件只填补空白、**不与专项审计重复双记**。

## 2. 需求（要什么）

- 范围内：
  - 拦 `/admin/v1` 下所有 POST/PUT/DELETE 写请求，在 handler 执行**之后**，对**未被专项审计覆盖**的 (method, RoutePattern) 补记一条 `audit_log`。
  - 补记字段：`operator`（从认证 context 取）、`action`（由 method + RoutePattern 推导）、`targetType` + `targetRef`（由资源词 + 路径参数推导）、`result`（响应 2xx=ok，否则 fail）、`clientIP`（来源 IP）。
  - **敏感豁免**：`detail` **绝不**写请求体；兜底审计 detail 留空（仅元数据）。
  - 覆盖集合：在中间件文件里维护一个**集中、带中文注释的常量集合**——「已被专项审计覆盖的写路由（method + RoutePattern）」。命中集合的请求不补记（避免双记）。
  - 审计落库失败只记 WARN，**绝不阻断主响应**（兜底审计是旁路）。
- 不做（范围外）：
  - 不改任何既有专项审计的位置 / 字段 / 时机。
  - 不把请求体 / 响应体 / 查询参数任何内容写进 detail（含非敏感字段也不写，保持兜底审计为纯元数据）。
  - 不为兜底审计引入新表 / 新字段 / 新依赖（复用 `audit_log` 与 `AuditLogRepository.Create`）。
  - 不做去重 / 限流 / 聚合（兜底是旁路、每写一条）。

## 3. 设计（怎么做）

涉及模块：仅控制面 `internal/server`（新文件 `audit_middleware.go` + 在 `router.go` 挂载）。复用 `internal/repository.AuditLogRepository`、`internal/model.AuditLog`、`internal/auth.Operator`、既有 `statusWriter`。**不碰 handler / service / repository 既有代码**。

- **中间件语义＝兜底 / gap-filling**：
  - 仅处理写方法（POST/PUT/DELETE/PATCH），GET 等读方法直接放行、不记。
  - 取当前请求的 `(method, chi RoutePattern)`；若**命中**覆盖集合 → 直接放行（专项审计已记，避免双记）；否则在 handler 执行后补记一条。
  - 用 `statusWriter` 包装 ResponseWriter 捕获状态码：2xx → `result=ok`，否则 `result=fail`。
- **action 推导**（纯函数，便于单测）：
  - 取 RoutePattern 末段静态词：若是已知特例动词（如 `/rollback`、`/offline`、`/drain`、`/reset`、`/promote`、`/confirm`、`/reverse-fetch`、`/imprint`、`/gray`），动词即该词；
  - 否则按方法映射：POST→`create`、PUT→`update`、DELETE→`delete`、PATCH→`update`。
  - 资源词 = RoutePattern 去掉 `/admin/v1/` 前缀后的**首个静态段**（如 `configs` / `instances`），单数化（去尾 `s`）。
  - `action = "<资源词单数>.<动词>"`（如 `widget.create`）。
- **target 推导**（纯函数）：
  - `targetType` = 资源词单数（如 `config` / `instance`）。
  - `targetRef` = RoutePattern 中路径参数按出现顺序用实际值拼接（如 `{id}` → 实际 id）；无路径参数则取资源词单数。
- **覆盖集合**：`map[string]struct{}`，键为 `"<METHOD> <RoutePattern>"`，集中列出当前所有已带专项审计的写路由（见文件内中文注释逐条标注对应 action）。新增写端点若已自带专项审计，**须把其 (method, pattern) 加入集合**；否则中间件会替它兜底补记。
- **挂载点**：`router.go` 的 `/admin/v1` 组内、`readonlyWriteGuard` **之后**（确保 context 已注入 operator、且被 readonly 拒写的请求不会进入兜底逻辑）。
- **clientIP**：`server` 包内置等价提取（X-Forwarded-For 首跳 → X-Real-IP → RemoteAddr host），与 handler 侧口径一致。

涉及架构：请求管线在 admin 组新增一道旁路中间件，不改分层方向（中间件属 server 层，落库经 repository，不碰 GORM 内部 / 内存结构）。无新 ADR（沿用 FR-7 审计模型与现有鉴权管线）。

## 4. 任务拆分

- [x] `internal/server/audit_middleware.go`：兜底中间件 + 覆盖集合 + action/target 推导纯函数 + clientIP
- [x] `internal/server/router.go`：在 `/admin/v1` 组 `readonlyWriteGuard` 之后挂载
- [x] `internal/server/audit_middleware_test.go`：未覆盖写端点补记 / 已覆盖端点不双记 / GET 不记 / 失败 result=fail / detail 无 body
- [x] 文档同步：API.md（/admin/v1 写操作统一审计兜底说明）、本规格

## 5. 验收标准

- 任取一个**未在覆盖集合内**的写端点，经中间件后 `audit_log` 新增一条，`operator`/`action`/`targetType`/`targetRef`/`result`/`clientIp` 正确。
- 任取一个**已在覆盖集合内**的写端点，中间件**不**额外补记（无双记）。
- GET 等读方法不产生兜底审计。
- 失败响应（非 2xx）兜底审计 `result=fail`。
- 兜底审计 `detail` 不含任何请求体内容（敏感豁免）。
- 审计落库失败时主响应不受影响（仅 WARN）。
- 受影响组件测试全绿（`go build ./...` + `go test ./...` + `go vet ./...`）。

## 6. 风险 / 待定

- 覆盖集合是手维清单：新增**自带专项审计**的写端点须同步加入集合，否则会被兜底重复记一条（弱重复、不致命）；反之新增**无专项审计**的端点会被自动兜底，符合预期。
- 当前所有既有写端点均已带专项审计，故中间件落地后对存量端点**零新增审计**；其价值在于对未来新端点的结构性兜底。
- 兜底审计 detail 一律为空（纯元数据），不区分端点；如需更细的 detail 由对应端点补专项审计承载，不在兜底职责内。
