# 功能规格：管理面只读角色 + 运行时 API 密钥

> 状态：开发中　·　关联 PRD：FR-42　·　分支：feature/admin-readonly-role-and-api-keys

## 1. 背景与目标

现有管理面鉴权（FR-11，[ADR-0009](../adr/0009-control-plane-auth-pulled-forward.md)）是单操作者模型：账号口令登录换 HMAC 令牌、凭据走 env 不落库、无角色、无主动吊销。外部业务管理后端需要用一把**只读**密钥调 `/admin/v1/*` 读取拓扑 / 实例 / zone 等事实，且**不具备任何写权限**。

目标（属 P2 治理增强）：引入 full / readonly 两级角色 + 可运行时创建 / 吊销 / 重置的 API 密钥 + 密钥操作审计，让外部服务以只读密钥安全接入，写端点一律 403。

## 2. 需求（要什么）

- 范围内：
  - **角色**：`full`（读写，等同操作者）/ `readonly`（只读）。`readonly` 仅可访问读端点（GET），任何写端点（发布 / 回滚 / 改派 / 创建 / 删除 / 吊销…）一律 403。"只读拒写"做成**鉴权中间件统一裁决**，不在各 handler 散落判断。
  - **API 密钥**：管理台"密钥管理"页——创建（名称 + 角色 + 可选过期）/ 列出（名称 / 角色 / 前缀 / 创建时间 / 最近使用 / 状态）/ 吊销 / **重置**。密钥经独立头 `X-Beacon-Api-Key` 或 `Authorization: Bearer` 认证，中间件解析角色后按读写裁决放行。
  - **只存哈希**：密钥落库只存 SHA-256 哈希，明文仅创建 / 重置时返回一次、**不可二次读取**（丢失只能重置轮换）；明文不入日志、不入审计 detail。
  - **审计**：密钥创建 / 吊销 / 重置写入既有 `audit_log`（operator / action / target / detail / result），管理台复用审计页过滤可查。
- 不做（范围外，需要再单独提）：
  - 细粒度资源 / 字段级权限、密钥按端点限 scope。
  - 自动 / 定时轮换（仅手动 reset）、速率限制、多租户。
  - 改动登录操作者模型（仍单操作者、凭据走 env）。

## 3. 设计（怎么做）

涉及模块：控制面 `apikey`（新叶子包：生成 + SHA-256 纯函数）/ `model`（`api_key` 表 + 角色 / 动作枚举）/ `repository`（密钥 CRUD）/ `service`（创建 / 吊销 / 重置事务 + 审计 + `Verify` 认证）/ `auth`（context 加 role）/ `server`（中间件认 API 密钥 + 只读拒写 guard + 路由）/ `handler`（密钥 CRUD，不碰角色）；前端 `ApiKeysPage` + 客户端 + 路由 / 导航。

- **认证**：中间件按 `X-Beacon-Api-Key` 或 `Authorization: Bearer`（`bk_` 前缀辨识）取密钥，经 `ApiKeyVerifier` 接口（service 实现）查库比对哈希 → 校验未吊销未过期 → 节流更新 `last_used_at`（库内时间戳自身节流，至多每分钟）→ 注入 `operator=apikey:<名称>` + 角色；登录令牌恒为 `full`。
- **授权**：`readonlyWriteGuard` 中间件统一裁决——`readonly` + 写方法 → 403。语义：401（未认证：缺 / 错 / 过期 / 吊销）vs 403（已认证但只读越权写）。
- **数据模型**：`api_key`（`key_hash` 唯一 + 软删哨兵、`key_prefix` 非机密片段、`role` VARCHAR、`expires_at` / `last_used_at` 可空、`deleted_at` 哨兵软删 = 吊销）。GORM 可移植、无方言专有类型。
- **简单优先**：不引 Redis / 会话存储 / OAuth / 鉴权框架；真源在库、查库比对哈希、吊销即时生效、不加进程缓存。

涉及架构决策已在 [ADR-0026](../adr/0026-runtime-api-keys-and-readonly-role.md) 记录，此处不重复决策正文。

## 4. 任务拆分

- [x] `internal/apikey`：明文生成 + SHA-256 哈希纯函数 + 单测
- [x] `internal/model`：`api_key` 实体 + 角色 / 动作 / 对象类型枚举 + AutoMigrate
- [x] `internal/repository`：密钥 CRUD（查活 / 列表含吊销 / 吊销 / 轮换 / 触达最近使用）+ 单测
- [x] `internal/service`：创建 / 吊销 / 重置事务 + 审计 + `Verify` 认证 + 单测（哈希 / 过期 / 吊销 / 重置 / 审计无明文）
- [x] `internal/auth` + `internal/server`：context 加 role + 中间件认密钥 + 只读拒写 guard + 路由 + 写方法判定单测
- [x] `internal/handler`：密钥 CRUD 处理器（视图剥离明文 / 哈希）
- [x] `internal/server` 集成测试：只读读 200 / 写 403、full 写 + 审计 operator、双请求头、过期 / 吊销 401、列表无明文
- [x] 前端：密钥管理页（创建 / 一次性明文展示 / 重置 / 吊销）+ 客户端 + 路由 / 导航 + dev mock
- [x] 文档同步：PRD FR-42、ARCHITECTURE 数据模型 / 鉴权、API.md 端点 / 鉴权头、ADR-0026、CHANGELOG

## 5. 验收标准

- 只读密钥访问 `GET /admin/v1/*`（instances/zones/configs/audits/metrics…）→ 200；访问任何写端点 → `403 FORBIDDEN`。
- 只读密钥经 `X-Beacon-Api-Key` 与 `Authorization: Bearer <bk_...>` 两种头均可认证。
- full 密钥可写；其写操作审计 `operator = apikey:<名称>`（非手填）。
- 创建返回明文一次；列表 / 详情无明文与哈希；库内只存哈希。
- 过期 / 吊销密钥访问 → 401；吊销 / 重置不存在密钥 → 404。
- 重置后旧明文立即失效、新明文可用（密钥只能重置、不能二次读取）。
- 密钥创建 / 吊销 / 重置写入 `audit_log`，detail 不含明文 / 哈希，管理台审计页按 `action=apikey.*` / `targetType=apikey` 可查。
- 受影响组件测试全绿（`go test ./...` 单元 + `-tags=integration` 集成；前端 `pnpm build` + `pnpm test`）。

## 6. 风险 / 待定

- 只读密钥可 `GET /admin/v1/api-keys`（仅元数据、无密钥明文），遵循"GET 一律放行"的统一裁决；若需密钥管理对只读完全不可见，属后续按需收紧。
- 单节点控制面无 HA，吊销 / 重置在同进程即时生效；多节点 HA 属 P3、届时密钥真源仍在库、各节点查库即一致。
