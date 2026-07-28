# ADR-0080：内置公网 MCP Streamable HTTP 与 OAuth Client Credentials 边界

**状态**：已接受（2026-07-29；在 MCP 范围内部分取代 [ADR-0026](0026-runtime-api-keys-and-readonly-role.md) 的“不使用 OAuth”结论）

## 背景

Beacon 需要让外部 Agent 完成观测、低风险写入和危险操作申请，以便自动化日常运维；所有危险操作仍必须由人类审批。直接把管理 REST 或数据库暴露给 Agent 会绕过领域校验、风险分类与审批，复用现有 `full` API key 又无法从协议和 audience 上限制凭据只用于 MCP。

MCP 应继续遵守 Beacon 的 Go + chi 单二进制、同端口和简单部署边界，同时具备公网机器到机器认证、明确的反向代理信任边界与可撤销客户端生命周期。

## 决策

### 1. MCP 直接内置于 Beacon

- Beacon 在现有 Go + chi 进程内提供 Streamable HTTP MCP resource，固定路径为 `/admin/v2/mcp`。
- 不新增 `/api/*` 前缀，不建立 sidecar、独立网关服务、消息队列或第二数据库。
- MCP handler 只做协议适配和 Principal 注入，调用既有 application service/query/operation adapter；禁止直接访问 repository、GORM、Agent 连接内部结构或通过 loopback HTTP 调管理 REST。
- 使用官方 MCP Go SDK 的稳定版本；如果稳定 SDK 的 OAuth 原语不足，才允许引入此前批准的一项成熟稳定 OAuth 库。不得为追最新规范使用预发布依赖。

### 2. 公网入口由受信反向代理终止 TLS

Beacon 后端不直接暴露公网 TLS。部署方在受信反向代理终止 TLS，并为 Beacon 配置唯一 `publicBaseURL` 与可信代理边界。服务端校验 Host、Origin、转发 scheme 和资源 URL，只接受可信代理提供的转发信息；设置请求体上限、并发/速率限制和超时，错误响应不得回显凭据。

OAuth/MCP 规范要求的 `.well-known` discovery 路径是协议发现入口，不构成第二套管理 API；业务 resource 始终是 `/admin/v2/mcp`。

### 3. OAuth Client Credentials 只服务 MCP

- 每个外部 Agent/集成使用独立 OAuth client；client secret 为高熵随机值，只存哈希，创建或轮换时明文仅返回一次。
- access token 短期有效、audience 精确绑定 MCP resource。MCP bearer 不得被普通 `/admin/v1/*` 或 `/admin/v2/*` REST 中间件接受。
- client 创建、轮换、重新启用属于危险操作并接入 [ADR-0079](0079-principal-capability-and-dangerous-operation-approval.md)；吊销属于止损，可直接执行并强审计。吊销 client 后其 token 立即失效。
- OAuth client 认证后映射为 `mcp` Principal，不建立与现有 API key 平行的通用 admin token。

### 4. 固定两种 MCP profile

- `observer`：读取元数据、指标与历史，不执行写操作。
- `automation`：包含 observer 能力、低风险直写，以及提交/查询/撤回自己审批申请的能力。

两种 profile 都不含 approve capability。该限制由 ADR-0079 的服务层授权不变量保证，而不是仅靠 MCP tools 列表隐藏。

### 5. 只暴露显式领域工具

每个 tool 对应一个稳定的查询或语义 operation descriptor，禁止任意 URL、任意 HTTP、SQL、文件路径或通用“调用管理 API”代理。低风险 tool 可直接调用领域服务；危险 tool 只能冻结输入并创建审批申请，返回 `approvalRequestId`，由 human 在 Beacon 审批中心批准并由后端自动执行。

读取数据库元数据、指标与历史可直接执行；任何会向 Agent 下发命令、读取实时日志/文件、返回敏感明文或 payload 的动作都按危险操作申请。工具层不得复制领域校验或审批状态机。

## 理由

- 同进程协议适配保持单二进制部署，不引入新的运维面和一致性问题。
- 独立 audience 使 MCP 凭据即使泄露也不能直接调用普通管理 REST。
- 每集成独立 client 可单独轮换、吊销和审计，适合外部 Agent 自动化。
- 显式领域工具比通用 HTTP 代理更易审计、版本化和绑定审批语义。
- TLS 交给反向代理符合现有部署方式，但必须显式限定可信代理，避免伪造转发头。

## 后果

- 需要新增 OAuth client/token 持久化、标准 metadata/token endpoint、客户端管理 API、审计和清理任务。
- 部署配置需要提供公网基址与可信代理设置；未正确配置时 MCP 公网入口必须 fail-closed，而不是猜测 URL。
- MCP 工具目录需要与 operation registry 建立覆盖测试；新增管理能力不会自动得到通用代理入口。
- 外部 Agent 需轮询审批结果；human 审批能力只存在 Beacon 管理面，不向 MCP 暴露。
- 现有 REST 登录与 API key 继续工作；[ADR-0026](0026-runtime-api-keys-and-readonly-role.md) 只在 MCP OAuth 范围被部分取代。

## 备选方案

- **复用 `full` API key 调全部 REST**：机器可绕过审批且凭据 audience 过宽。否决。
- **MCP sidecar 调 Beacon REST**：产生第二部署单元、重复鉴权与潜在审批递归。否决。
- **提供任意 HTTP/SQL 工具**：无法保证领域不变量和危险操作覆盖。否决。
- **让 MCP Agent 审批自己的申请**：等同取消人审。否决。
- **Beacon 直接承担公网 TLS**：增加证书生命周期和部署复杂度；本期由受信反向代理负责。否决。
- **使用官方 SDK 预发布版**：协议覆盖可能更新，但依赖稳定性不符合本项目要求。否决。
