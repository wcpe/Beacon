# 功能规格：管理面鉴权

> 状态：开发中　·　关联 PRD：FR-11　·　分支：feature/admin-auth

## 1. 背景与目标

控制面 admin API 当前完全无鉴权，`/admin/v1/*` 未挂任何鉴权中间件。本批要做"三方插件文件覆盖 + agent 受限重载命令"（FR-15），若在无鉴权系统上开放命令执行，等于开放对所有子服的远程命令执行面。按 [ADR-0009](../adr/0009-control-plane-auth-pulled-forward.md)，将原 FR-11 中的**管理面鉴权**自 P2 前移本批；配置加密拆为 FR-20 留后期。

目标：给管理台操作者加最小鉴权（登录→令牌→中间件校验），写操作授权，操作者身份从认证态取并入审计（取代前端手填的 operator）。

## 2. 需求（要什么）

- 范围内：
  - 登录端点：操作者凭据（用户名/口令）→ 令牌；凭据来源走 env/配置，禁硬编码。
  - 令牌校验中间件挂在 `/admin/v1/*`（登录端点自身豁免）；无/错令牌 → 401。
  - 写操作（发布/回滚/改派/下线/新建/软删等）授权校验；操作者身份从认证态取，纳入既有 `audit_log`，后端以认证身份为准（忽略请求里手填的 operator）。
  - agent 侧 `/beacon/v1/agent/*` 的共享 token 维持不变（仅防误连，不动其语义）。
- 不做（范围外）：
  - 角色/权限矩阵（RBAC）、多操作者账户体系。
  - 配置加密（FR-20）。
  - 前端登录 UI（FR-18）。
  - agent 命令执行（FR-15，鉴权落地后才解 gate）。

## 3. 设计（怎么做）

涉及模块：控制面 `config` / `auth`（新增叶子包）/ `server`（中间件+路由）/ `handler`（登录处理器 + 写操作取认证身份）/ `service`（写操作 operator 来源改为认证身份）。

- **令牌机制**：无状态 HMAC-SHA256 签名令牌，stdlib 实现，不引第三方库、不落库、不引 Redis（遵架构不变量 §2 简单优先）。令牌载荷 = `operator|expiresUnix`，签名用配置密钥。中间件用恒定时间比较校验签名与有效期。
- **凭据来源**：`config.Auth`（用户名/口令/签名密钥/令牌有效期秒）；口令与密钥经 env 注入（`BEACON_ADMIN_USERNAME` / `BEACON_ADMIN_PASSWORD` / `BEACON_AUTH_SECRET`），禁硬编码、禁入库。
- **认证身份入审计**：写操作处理器从认证态（context）取 operator 传入 service，替换请求体/查询参数里的 operator 字段。
- **DB 可移植**：单操作者凭据走配置即可，不新增数据库表（最小范围，不镀金）。

涉及架构决策已在 ADR-0009 记录，此处不重复决策正文。

## 4. 任务拆分

- [ ] `internal/auth`：令牌签发/校验纯逻辑 + 单测（红→绿）
- [ ] `internal/config`：Auth 配置项 + env 覆盖 + 校验
- [ ] `internal/server`：authMiddleware + 路由挂载（login 豁免）
- [ ] `internal/handler`：auth 登录处理器 + 写操作改取认证身份
- [ ] `internal/server` 集成测试：无令牌 401 / 登录 / 有令牌通过 / 写操作授权
- [ ] 文档同步：PRD 状态、ARCHITECTURE、API、CHANGELOG

## 5. 验收标准

- 无 `Authorization` 令牌访问 `/admin/v1/*`（登录端点除外）→ 401 UNAUTHORIZED。
- 正确凭据登录 → 200 返回令牌；错误凭据 → 401。
- 携带有效令牌访问 admin 写/读端点 → 通过。
- 写操作审计的 operator = 认证身份（非请求手填）。
- agent 侧 `/beacon/v1/agent/*` 行为与语义不变。
- 受影响组件测试全绿（`go test ./...` 单元 + `-tags=integration` 集成）。

## 6. 风险 / 待定

- 令牌为无状态签名令牌，注销/吊销需重启或换密钥（最小范围可接受；如需主动吊销属后续增强）。
- 单操作者模型：多账户/RBAC 属后续需求，本批不做。
