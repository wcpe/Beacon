# 功能规格：内置 `/admin/v2/mcp` 与 OAuth Client Credentials

> 状态：草拟　·　关联 PRD：FR-219　·　分支：待执行时创建　·　依赖：FR-206、FR-207

## 1. 背景与目标

Beacon 需要让外部 Agent 以机器身份完成远程观测和运维申请，但现有登录令牌与 API key 的受众均是普通管理 REST，不能直接作为 MCP 公网凭据。MCP 必须内置在现有控制面进程中，保持单二进制部署，并把每个外部集成映射为可独立吊销、轮换和审计的机器主体。

本规格落实 [ADR-0080](../adr/0080-builtin-mcp-oauth-client-credentials.md)：在 `/admin/v2/mcp` 提供 Streamable HTTP MCP resource；使用 OAuth Client Credentials 颁发只对该 resource 有效的短期 token；公网连接必须经受信 TLS 反向代理。主体和危险操作边界以 [ADR-0079](../adr/0079-principal-capability-and-dangerous-operation-approval.md) 为准。

成功标准：标准 MCP 客户端可完成鉴权、初始化、工具发现与调用；MCP bearer 无法调用普通管理 REST；吊销客户端后已颁发 token 立即失效；错误、日志和审计均不泄露 secret/token。

## 2. 范围与非目标

### 2.1 本期范围

- Beacon 同进程、同端口托管 Streamable HTTP MCP。
- OAuth Client Credentials 的客户端生命周期、token 端点、resource metadata 与审计。
- 固定 `observer`、`automation` 两种 profile，并映射 FR-206 的 `mcp` Principal 与 capability bundle。
- 受信反向代理、公网基址、Host/Origin、限流、超时和请求体边界。
- 为 FR-220 提供经过认证的 MCP session 与 tool registration 容器。

### 2.2 明确不做

- 不新增 `/api/*` 路由、sidecar、独立 MCP 服务、第二数据库、Redis 或消息队列。
- 不让 MCP bearer 访问 `/admin/v1/*`、除 MCP/OAuth 协议端点外的 `/admin/v2/*` 管理 REST。
- 不支持 Authorization Code、Device Code、refresh token、动态客户端注册或第三方身份联合。
- 不提供 `full` profile，也不向任何 MCP client 分配批准/驳回能力。
- 不在本 FR 定义具体领域工具；工具清单与审批交接由 FR-220 定义。
- 客户端生命周期动作以管理 API 为准；管理台提供独立 MCP 客户端页（`/mcp-clients`）承载日常运维（列表、创建、轮换、启用、吊销），并保留 FR-212 审批中心对申请记录的展示。该页只消费上述管理 API，不新增第二套业务端点。

## 3. 总体设计

### 3.1 进程与依赖边界

- 在现有 chi router 注册 `/admin/v2/mcp`，GET、POST、DELETE 等方法按稳定版 Streamable HTTP SDK 的协议要求处理。
- MCP handler 只负责协议、token 验证、session 与 `Principal` 注入；工具调用进入 application service，不经 loopback HTTP，不直接访问 repository/GORM/Agent 连接表。
- 优先使用官方 MCP Go SDK 的稳定版本。若实施期依赖审查证明其 OAuth 原语不足，只允许增加一项已批准的成熟稳定 OAuth 库；禁止预发布依赖和重复功能依赖。
- SDK 或 OAuth 库版本必须在实施计划中锁定并通过依赖与许可证检查，本规格阶段不改构建文件。

### 3.2 固定路由

| 方法 | 路径 | 用途 |
|---|---|---|
| MCP 协议所需方法 | `/admin/v2/mcp` | Streamable HTTP resource，固定 audience |
| POST | `/admin/v2/oauth/token` | `client_credentials` 换取 MCP access token |
| GET | 协议规定的 `.well-known` resource metadata | 只发布 `/admin/v2/mcp` 与授权服务器元数据 |
| GET | 协议规定的 `.well-known` authorization server metadata | 只发布 token endpoint 与支持的 grant |
| GET | `/admin/v2/mcp-clients` | 人类管理端查询客户端全量列表（按创建时间倒序） |
| GET | `/admin/v2/mcp-clients/{id}` | 人类管理端查看客户端摘要与审计引用 |
| GET | `/admin/v2/mcp/config` | 人类管理端只读查看 MCP 入口部署配置（启动项，不可热改） |
| POST | `/admin/v2/mcp-clients` | 申请创建客户端，危险操作 |
| POST | `/admin/v2/mcp-clients/{id}/rotate` | 申请轮换 secret，危险操作 |
| POST | `/admin/v2/mcp-clients/{id}/enable` | 申请重新启用，危险操作 |
| POST | `/admin/v2/mcp-clients/{id}/revoke` | 立即吊销，止损动作 |

`.well-known` 仅是标准发现入口，不构成第二套业务 API。不得同时提供 `/api/v1/mcp`、`/api/v2/mcp` 或根级 `/mcp` 别名。

## 4. OAuth 客户端与令牌

### 4.1 `mcp_oauth_client`

| 字段 | 约束 | 语义 |
|---|---|---|
| id | 内部数值主键 | 数据库关联，不对操作者赋予业务语义 |
| client_id | 全局唯一、创建后不可改 | 高熵公开标识 |
| display_name | 可改、可重复 | 人类识别名称 |
| secret_hash / secret_prefix | 非空 | 只存 SHA-256 哈希与非敏感前缀 |
| profile | `observer` / `automation` | 固定 capability bundle |
| status | `active` / `revoked` | `revoked` 不能换 token；重新启用需审批 |
| secret_version | 单调递增 | 轮换后使旧 secret 永久失效 |
| created_by / created_at / updated_at / revoked_at | 审计字段 | 关联人类申请人与生命周期 |

另设领域内 `mcp_oauth_client_change` 保存待应用创建/轮换：`change_id`、clientId/目标客户端、`secret_hash`、前缀、目标版本/profile、`pending|applied|invalidated`、唯一 approvalRequestId 与时间。它不是通用 credential 表，也不参与认证；只有 active client 行可换 token。

创建和轮换申请在返回 `202` 前生成高熵 secret，并在同一事务把哈希写入不可变 pending change；通用审批载荷只冻结 changeId、clientId、非敏感前缀、目标版本/profile，绝不保存 secret/hash。明文只在该次响应返回一次，服务端不持久化明文。审批被拒绝、撤回或过期时 change 变为 invalidated，该 secret 永远不能生效；批准后持久执行器原子应用同一 change 并标 applied。调用方遗失明文只能重新申请轮换，worker 重试不得生成第二份 secret/change。

创建、轮换、重新启用的响应为 `{approvalRequestId, clientId, clientSecret, status, expiresAt}`，其中 `clientSecret` 只在首次响应存在。查询审批与客户端接口均不得再次返回明文。吊销为 `direct + 强审计`，重新启用为 `approval_required`。

### 4.2 `mcp_access_token`

access token 使用高熵不透明随机值，库内只存哈希，至少记录 `client_id`、`secret_version`、固定 audience、profile/capability 快照、issued_at、expires_at 与 revoked_at。token 有效期为服务端固定短周期且不得超过 15 分钟，不签发 refresh token。

每次验证同时检查：token 未过期/未撤销、客户端仍 active、secret_version 未变化、audience 精确等于规范化后的公开 `/admin/v2/mcp` resource。轮换或吊销客户端后，旧版本 token 即时失效；过期 token 由批处理清理，查询和清理不得把全部历史一次性加载入内存。

### 4.3 token 请求

`POST /admin/v2/oauth/token` 仅接受 `application/x-www-form-urlencoded`：

- `grant_type=client_credentials`；
- `client_id`、`client_secret` 必填；
- `audience` 必须等于 metadata 发布的 MCP resource；
- 可选 scope 只能缩小 profile 已有能力，不能扩大；未传时使用完整 profile bundle。

成功响应遵循 OAuth token response，`token_type=Bearer`，包含 `access_token`、`expires_in` 与实际 scope。认证失败统一返回协议错误，不区分 clientId 不存在、secret 错误、已吊销等内部原因，并按 clientId/来源维度限速。

## 5. Principal 与授权

- token 验证成功后构造 `type=mcp`、`principalId=client_id`、profile、capability snapshot 与 traceId；客户端不能通过请求参数覆盖主体。
- `observer` 只有 FR-220 登记的读取能力。
- `automation` 包含 observer、低风险领域写入和提交/查询/撤回本人审批申请能力。
- 两种 profile 都没有 `approval.approve`、`approval.reject` 或任何等价能力；FR-206 服务层必须再次拒绝所有非 human 主体，不能只依赖工具隐藏。
- MCP bearer 进入普通 REST auth middleware 时统一返回 401/受众不匹配；现有登录 token、API key 进入 MCP resource 同样返回 401。

## 6. 公网与反向代理安全边界

- 部署必须显式配置唯一 `publicBaseURL`、可信代理来源范围与 MCP 是否启用；缺失、非 HTTPS 公网基址或请求未来自可信代理时 MCP 和 token endpoint 失败关闭。
- 只有可信代理来源的标准转发 scheme/host 可参与公开 URL 校验；任意客户端伪造的 `X-Forwarded-*` 不得改变 audience、resource metadata 或审计来源。
- 校验 Host、Origin、Content-Type、协议版本和 session 标识；设置请求体上限、最大并发、每客户端速率、读写/空闲超时。
- 反向代理负责 TLS 与必要的流式转发配置；Beacon 后端监听地址不得直接暴露公网。运维文档在实施期给出最小反代示例和验证命令。
- CORS 不使用通配符；错误体、日志和指标标签不得包含 client_secret、access_token、Authorization 或完整敏感参数。

## 7. 审计与可观测性

- 审计事件至少包括 `mcp.client.create_requested/created/rotate_requested/rotated/enabled/revoked`、`mcp.token.issued/denied`、`mcp.session.opened/closed`。
- token 成功签发只记录 clientId、profile、过期时间、来源摘要和 traceId；失败仅记录归一化原因，不记录 secret/token。
- 指标按 endpoint、结果和 profile 聚合；clientId 不作为无界指标标签。认证失败日志按限速聚合，使用中文 WARN，不刷屏。
- client 与 token 记录、审批申请和工具审计通过 principalId/traceId 可关联；删除历史审计不属于本 FR。

## 8. 向后兼容与发布顺序

1. 完成 FR-206 Principal/capability 与 FR-207 审批执行内核。
2. 实施 OAuth client/token 持久化、哈希原语与管理 API；先保持 MCP disabled。
3. 接入稳定 MCP SDK、metadata/token endpoint 与空工具容器。
4. 完成 audience 隔离、可信代理、Origin/Host、限流与脱敏测试。
5. 实施 FR-220 工具目录后再允许启用 MCP；没有已登记工具时不得宣称可自动化运维。
6. 更新 API、ARCHITECTURE、OPERATIONS、SECURITY、CHANGELOG，再进行 TLS 反代与真实外部客户端验收。

现有登录令牌与 API key 行为保持兼容；不得把 OAuth 改造扩散成所有管理 REST 的认证迁移。

## 9. 实施任务拆分

1. 依赖盘点、稳定 SDK 选型与许可证审查。
2. OAuth client/token 模型、迁移、repository、随机值与哈希服务。
3. 客户端创建/轮换/启用审批 adapter、吊销止损动作和审计。
4. token endpoint、metadata、audience 验证与 MCP Principal 中间件。
5. `/admin/v2/mcp` Streamable HTTP session 与协议错误映射。
6. 可信代理、公网 URL、Host/Origin、限流、超时和脱敏门禁。
7. 自动化测试、外部 MCP 客户端、反代和重启恢复验收。
8. 实施期同步权威文档并完成安全复核。

## 10. 测试与验收

### 10.1 自动化测试

- 创建/轮换只返回一次明文；secret/hash 均不进入通用审批载荷，哈希只在领域 pending change/active client；拒绝/撤回/过期 change 永不生效，worker 重试不生成第二份 secret。
- Client Credentials 正常、错误 secret、错误 audience、错误 grant、scope 扩权、吊销和轮换旧版本覆盖。
- token 上限 15 分钟、过期、即时吊销、重启后验证与分批清理。
- MCP bearer 调普通 REST、登录 token/API key 调 MCP 均被拒绝。
- metadata 只发布 HTTPS 公网 resource 和 `/admin/v2/oauth/token`；不存在 `/api/*`、`/mcp` 别名。
- 不可信代理头、错误 Host/Origin、超限 body、并发/速率/超时、错误脱敏覆盖。
- `initialize`、session 生命周期和协议错误在稳定 SDK 支持的客户端矩阵通过。

### 10.2 真实环境验收

- 使用 TLS 反向代理和独立 `observer`、`automation` 客户端各完成一次换令牌与 MCP 初始化。
- 从公网只访问反代地址，后端直连被网络与应用门禁共同阻断。
- 吊销客户端后，现有 session 的下一次受保护调用失败，新 token 无法签发。
- 轮换后旧 secret/token 失效，新 secret 只由申请响应展示一次。
- 日志、审计、数据库抽查不出现明文 secret/token。

## 11. 风险与收口条件

- Streamable HTTP 与 OAuth 规范会演进；实施时只能选稳定 SDK，并把协议兼容矩阵锁入测试，不能以预发布依赖追新。
- 可信代理配置错误会造成 audience 混淆或头伪造；未完成真实反代验收前 MCP 必须默认关闭。
- “测试能连接”不等于外部自动化可用；FR-220 工具和危险操作审批交接全部验收前，FR-219 不得标已交付。
- 新依赖、构建配置与部署配置的实际改动必须在实施任务中单独取得所需确认；本次只冻结规格。

## 12. 已拍板决定

- MCP 直接内置 Beacon，不部署 sidecar。
- resource 固定为 `/admin/v2/mcp`，不创建 `/api/*` 前缀。
- 公网只经 TLS 反向代理，Beacon 后端不直接公开。
- 使用 OAuth Client Credentials，每个外部 Agent 独立 client、短期 audience-bound token、可撤销轮换。
- 固定 `observer`、`automation` 两种 profile，机器主体永远不能审批。
- 只使用官方稳定 MCP Go SDK；确有缺口时最多增加一项成熟稳定 OAuth 库。
- **（2026-09-21 修订）** 管理台提供独立 MCP 客户端页 `/mcp-clients`：外部集成数量会随接入方增长，纯靠审批中心无法回答"现在有哪些客户端、各自什么 profile、谁被吊销了"。该页只消费上文既有管理 API，不新增业务端点，也不改变"机器主体永不审批"的分权设计。原"不新增独立页面"的决定由本条取代。
