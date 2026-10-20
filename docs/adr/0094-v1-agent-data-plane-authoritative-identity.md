# ADR-0094：v1 agent 数据面以 DB 权威身份替代共享 token 信任

**状态**：已接受（2026-10-20；取代 [ADR-0009](0009-control-plane-auth-pulled-forward.md) 中「agent 侧共享 token 维持现状、不升级为安全边界」的条款，并取代 [ADR-0040](0040-agent-readonly-log-tail.md) 决策 4、[ADR-0042](0042-admin-api-token.md) 中关于共享 token 非安全边界的表述）

## 背景

v1 agent 数据面（`/beacon/v1/agent/*`）原先只挂一个共享 token 中间件，全集群 agent 共用同一把凭据，而所有端点的 `namespace` / `serverId` 由请求体或 query 自报。这带来两个问题：

- token 通过只代表「某台已接入的 agent」，不代表「自称的那台」。任一持有该 token 的实例可以顶替他人心跳与上报、读走他人的有效配置与文件正文、抢走他人的待办命令；
- 回传类端点（ingest / scan / error / 命令结果 / 日志 / 目录浏览 / file-sync）只凭自增可枚举的 `commandId` 或 `taskId` 就接受写入，可以覆盖或推进他人的命令与任务。

[ADR-0009](0009-control-plane-auth-pulled-forward.md) 当年把共享 token 明确定位为「仅防误连」，前提是数据面不需要防横向冒充。随着身份体系（v2 注册 + 管理台确认 + boot 绑定）落地，该前提已不再成立；[agent 最小接入与身份迁移规格](../specs/agent-minimal-bootstrap-and-identity-migration.md) 也已规定数据面应以 DB 权威 `namespace` / `serverId` 为准、不信任自报值，但实现一直未跟上。

## 决策

1. v1 数据面的心跳 / 上报 / 声明 / 服务发现 / 配置与文件读取 / 命令拉取 / 各类回传统一要求权威身份：调用者携带 `X-Beacon-Token`（须为其所属命名空间的 access token）与 `X-Beacon-Identity`；`X-Beacon-Boot` 用于双实例冲突检测与陈旧 boot 识别（经身份分支的请求必须携带，服务层单请求校验仅在 boot 非空时比对，以兼容不带该头的存量上报路径），服务端据 DB 绑定解析身份。
2. 请求自报的 `namespace` / `serverId` 只作为**待校验的声明**，与身份不一致即拒绝；发现端点的 namespace 缺省时收敛到身份所属环境，不再返回全环境拓扑。
3. 回传类端点在 service 层比对命令归属，file-sync 另比对任务归属（blob 上传限源实例、下载限计划内目标）与 URL `taskId` 和命令载荷的一致性。
4. 身份与归属不符**统一返回 404**，与「命令/任务不存在」同码同文案，不使用 403——后者等于确认「该资源确实存在但不属于你」，可被用于枚举。唯一例外是注册端点：它比的是调用方**对自己**的声明，不涉及他人资源存在性，故复用 `IDENTITY_BINDING_MISMATCH`（409）以便运维一眼看出是部署参数填错。
5. 注册端点保留共享 token 作为引导通道（此时可能尚无身份）；但若请求经 v2 身份分支到达（即已携带可校验的已确认身份），其自报 namespace / serverId 必须与 DB 权威绑定一致，不一致按 409 `IDENTITY_BINDING_MISMATCH` 拒绝。机器注册通道（`mcp.allow-machine-register` 开关语义）不变，仍以共享 token 判定「受信内部调用方」。
6. 能力模型不变：本次不含 namespace 级授权，具备 `management.read` 的主体仍可读其可观测范围内的正文（延续 [ADR-0093](0093-direct-sensitive-content-reads.md)）。

## 理由

- 身份取自 DB 权威绑定而非请求自报，是唯一能消除横向冒充的来源；共享 token 作凭据无法区分实例。
- 归属不符复用 404，避免把校验变成存在性探针；与既有「命令不存在」口径一致，不新增错误码分支。
- 注册端点的归属不符走 409 而非 404：该判断只涉及调用方对自身的声明（不暴露他人资源是否存在），而注册失败是部署期配置错误，用 `IDENTITY_BINDING_MISMATCH` 能让运维直接定位，无需猜「是不是环境名写错了」。
- 引导阶段（注册）保留共享 token，避免「需要身份才能注册、需要注册才有身份」的死锁；但已携带可校验身份时仍以 DB 绑定为准，不让引导通道成为绕过归属校验的后门。

## 后果

- **破坏性变更**：数据面要求 namespace access token + 已确认身份。若部署仍把 `beacon.bootstrap-token` 填为历史全局共享 token，升级后数据面全部失败（401/403/409）。
- 失败的 agent 只会按心跳周期静默重试，**不会进入降级态**；其中 401 / 403 / 409 只重试、不重注册，而归属不符与陈旧 boot 的 **404 会触发 agent 重新注册**（日志表现为反复 register）。因此升级前必须逐项确认 token 类型与身份状态；该前提已写入 `docs/OPERATIONS.md` 的升级章节。
- 每次数据面请求多一次身份解析（含 boot 观测），与 v2 上报面同源；集群规模较大时需关注该路径的数据库压力。
- 归属校验只解决「冒充与误读」，不构成 namespace 级租户隔离：系统内不存在比「已认证」更细的读权限模型。

## 备选方案

- **维持共享 token、仅按请求参数定位**：即改造前现状，无法阻止横向冒充，否决。
- **为 v1 全面引入 namespace 级 ACL**：超出本次范围，且需要先有主体到 namespace 的授权模型（当前 `management.read` 是全局能力），留待后续。
- **要求签名而非身份查询**（每个实例持独立密钥）：更彻底，但需要分发与轮换机制，且与既有 v2 身份体系重复，否决。
