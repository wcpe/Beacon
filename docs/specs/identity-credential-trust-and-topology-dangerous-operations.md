# 功能规格：身份、凭据、信任与拓扑危险操作适配

> 状态：开发中　·　关联 PRD：FR-208　·　依赖：FR-205～207、[ADR-0079](../adr/0079-principal-capability-and-dangerous-operation-approval.md)

## 1. 背景与目标

Agent 身份确认/解绑、API key 与 namespace token 生命周期、跨 namespace 信任、服务器分配/换区和 LobbyCluster 迁移目前分别使用直接调用、二次确认或各自的人工确认。只把这些按钮移到审批页会留下 REST V1/V2、MCP、后台调用和最终 service 旁路，也会把原领域状态机复制成多套审批状态机。

本规格只为上述领域登记 operation descriptor 与小型 adapter。FR-207 是唯一审批真源；身份、凭据、信任和拓扑继续持有自身状态与事务不变量。扩大权限、恢复能力或改变运行拓扑必须经 human 批准；撤销能力、禁用身份和进入排空等立即止损动作可直接执行，但必须强审计。

## 2. 范围与非目标

### 2.1 本期范围

- AgentIdentity 确认、解绑、重新启用、占用冲突处置与拒绝/禁用。
- namespace token、管理 API key 与 MCP OAuth client 的创建、轮换、启用和吊销审批接缝。
- namespace trust 的授予、扩大、重新授予与撤销。
- server 首次分配、换区、默认入口、LobbyCluster 成员迁移、排空恢复等拓扑动作。
- 既有 V1/V2/REST/MCP/内部调用统一映射同一 operation，危险 service 强制 `ExecutionPermit`。
- 领域页面的申请入口、影响预览、审批跳转与止损反馈。

### 2.2 明确不做

- 不复制 approval_request、24 小时过期、worker、决策或 ExecutionPermit 实现。
- 不重写 AgentIdentity、server rezone、namespace trust、LobbyCluster 或 credential 的领域状态机。
- 不把读取、displayName/描述修改、空草稿编辑等低风险动作全部升级审批。
- 不把 Agent 命令、实时日志、文件或敏感内容读取放在本 FR；由 FR-209 负责。
- 不实现 namespace/server 归档和墓碑；由 FR-215～218 负责。
- 不允许 system policy、API key 或 MCP client 通过 adapter 批准申请或构造 ExecutionPermit。

## 3. 操作分类

### 3.1 统一矩阵

| 领域 | operation | 分类与理由 |
|---|---|---|
| 身份 | `identity.approve` | `approval_required`；建立可信 namespace/serverId 绑定并进入 active |
| 身份 | `identity.unbind` | `approval_required`；破坏现有绑定与调度资格，禁用是更快的止损替代 |
| 身份 | `identity.resolve_conflict` | `approval_required`；保留一方会永久排除另一实例 |
| 身份 | `identity.enable` / `identity.allow_reapply` | `approval_required`；恢复接入或重新申请能力 |
| 身份 | `identity.disable` / `identity.reject_pending` | `direct + 强审计`；只降低权限/运行资格 |
| 凭据 | `credential.create` / `credential.rotate` / `credential.enable` | `approval_required`；产生或恢复可用认证能力 |
| 凭据 | `credential.revoke` | `direct + 强审计`；立即止损，旧凭据/令牌即时失效 |
| 信任 | `namespace_trust.grant` / `expand` / `regrant` | `approval_required`；建立或扩大跨 namespace 能力 |
| 信任 | `namespace_trust.revoke` | `direct + 强审计`；只收缩跨域能力 |
| 拓扑 | `topology.server_assign` / `server_rezone` | `approval_required`；改变运行目标与身份分配事实 |
| 拓扑 | `topology.default_entry.change` | `approval_required`；改变玩家兜底入口集合 |
| 拓扑 | `topology.lobby_member.move` | `approval_required`；在 Zone/Lobby/未分配之间迁移 |
| 拓扑 | `topology.draining.disable` | `approval_required`；恢复调度、扩大运行影响 |
| 拓扑 | `topology.draining.enable` | `direct + 强审计`；停止接收新流量的止损动作 |

创建空的 env/BC/大区/小区、修改 displayName/描述等不产生认证或运行副作用的动作继续按 FR-206 的 `direct` 分类。namespace 创建若同时生成可用 token，必须作为 `credential.create` 复合危险操作审批，不能先直建后绕过凭据审批。

### 3.2 分类不变量

- “撤销/禁用/暂停”只有在不自动触发恢复、迁移、重启或扩大目标时才是 direct；同一路由 body 含扩权分支时必须拆成不同 operation。
- V1/V2 同义入口、MCP tool 与内部任务指向同一 descriptor/adapter。新增入口未分类时启动/测试失败，运行时 fail-closed。
- `system_exempt` 不得映射本表任一危险 operation。危险后台任务只能消费 human 已批准的持久任务。
- 领域 service 最终产生副作用的方法必须以 ExecutionPermit 为必填参数；direct 止损方法使用独立 capability + 强审计，不伪造 permit。

## 4. 影响快照与 adapter

### 4.1 身份

`IdentityApprovalAdapter` 冻结 identityId、namespace id/code、申请/当前 serverId、kind、当前状态/bootId、占用者、预填拓扑目标、资源版本和冲突摘要。批准执行仍调用既有 T3/解绑/冲突处置事务，不能在通用审批 service 重写状态转换。

- 旧 `POST /admin/v2/agent-identities/{identityId}/approve` 变为兼容申请入口，返回 `202 + approvalRequestId`，批准后 adapter 自动执行 T3。
- 影响快照或占用者、bootId、目标归属变化时终态 `failed`，要求重新申请；不得按当前值静默改批。
- `identity.disable` 立即使接入/调度失败关闭；`identity.enable` 必须新建审批，不能由同一止损调用自动反转。

### 4.2 凭据

创建/轮换申请只冻结角色、有效期、目标与脱敏元数据，绝不生成或保存凭据明文/哈希。批准 worker 在领域事务内生成明文、安装哈希，并把待领取明文以独立密钥加密为一次性密文；通用审批申请、审计和日志均不保存凭据哈希或明文。

- 批准 worker 校验 pending change 与 requestId/版本/状态一致后，原子安装其中的哈希并标记 applied；拒绝、撤回或过期把 change 标记 invalidated，该明文永不生效。worker 重试只消费同一 change，不生成第二份 secret。
- 库内只允许凭据领域 pending change/活动凭据保存哈希；通用审批表、审批快照、审计和日志都不能保存凭据哈希或明文 secret/token。遗失只能重新申请轮换。
- 吊销直接写不可逆/即时失效事实并强审计；重新启用不得隐式复用已明确作废的凭据，须按该凭据领域既有安全规则重新生成或显式恢复，并走审批。
- API key、namespace token 与 MCP client 保持各自表和认证协议，不建立通用 credential 上帝表。
- 申请成功后不能返回凭据明文。批准执行成功后，仅申请该请求的 human 可调用一次性兑换端点领取明文；兑换必须原子标记已领取，后续同一请求或其他主体一律返回 `credential_secret_lost`，明文绝不写入审批真源、审计或日志。

### 4.3 信任

申请冻结 from/to namespace 稳定 code、capability 集、方向、当前 trust 版本、影响到的调度/消息/Agent 能力摘要与原因。批准只授予快照中的能力；执行时发现 namespace archived/tombstoned、现有 trust 变化或 capability 集漂移则失败。

撤销可直接执行并强审计，立即阻断新跨域行为；不得因撤销失败自动重授。恢复/扩大需新申请，不能复用旧批准事实。

### 4.4 拓扑

申请冻结 server 内部 id/serverId、identity、namespace、当前与目标 BC/Region/Zone/LobbyCluster 稳定路径、在线/健康/排空/默认入口、活动交付/任务冲突、目标全集排序哈希和原因。

- 批准后 adapter 调用既有 server assignment/rezone/Lobby 事务与 drain/default-entry service，不复制归属写逻辑。
- 目标父链、身份绑定、排空状态、Lobby/Zone 互斥、活动 ChangeOrder 或目标全集漂移时失败关闭。
- 批量操作全集由服务端冻结并排序；页面分页样本不能成为批准范围。

## 5. API 与兼容契约

领域页先调用有界影响端点，再使用统一申请 API；现有危险 POST/PUT 可作为兼容申请入口，但不得直接执行：

| 领域 | 影响/申请契约 |
|---|---|
| identity | 现有 identity 详情提供冻结预览；approve/unbind/enable/conflict 入口返回统一申请 |
| credential | create/rotate/enable 返回 `{approvalRequestId, status}`；成功后申请该请求的 human 可调用一次性兑换端点领取明文 |
| trust | grant/expand/regrant 只创建申请；revoke 原端点 direct + 强审计 |
| topology | server assignment/rezone/default-entry/Lobby move 返回申请；draining=true direct，false 返回申请 |

统一错误体 `{code,message,traceId}`。常见错误包括 `operation_requires_approval`、`approval_target_changed`、`credential_secret_lost`、`identity_state_changed`、`topology_target_changed`、`active_operation_conflict`。兼容入口与 `/admin/v2/approval-requests` 必须使用同一 Idempotency-Key 和申请 ID，不维护领域审批表。

兑换端点为 `POST /admin/v2/approval-requests/{requestId}/credential-secret/redeem`。它只接受该请求的 human requester，且仅当审批已成功并关联可领取凭据时返回一次 `{secret}`；任何非 human、非申请人、非成功请求、重复领取或已失效凭据均返回 `410 credential_secret_lost`，不得返回 secret。

## 6. UX / 交互

- 用户任务：在身份、凭据、信任、区服或 Lobby 页面查看影响并提交危险申请；事故时可立即撤销/禁用/排空；human 到审批中心批准并自动执行。
- 进入路径：保留现有领域入口，危险按钮改为“申请…”，提交后深链 `/approvals/{id}`；止损按钮原地执行并给审计链接。
- 操作闭环：影响预览 → 原因/目标确认 → 提交 → 审批中心比较快照与当前事实 → 批准并执行 → 返回领域页查看结果；失败只能按当前事实重提。
- IA：不新增综合领域页面；全局待办与历史只在 `/approvals`，各领域继续展示自身状态。

页面四态：

- **空态**：无待确认身份、无可轮换凭据、无可授予信任或无合格目标时说明前置条件，不生成空申请。
- **加载态**：当前事实、影响预览和冲突检查分别显示骨架；未形成服务端冻结摘要前禁用提交。
- **错误态**：状态/目标漂移、凭据一次性明文遗失、审批失败或 direct 止损失败显示脱敏原因和重提/重试路径，不假装已生效。
- **超大量态**：身份、凭据、trust 和拓扑目标均服务端分页/搜索；批量审批冻结完整集合 hash，浏览器只展示计数与分页样本。
- **常规态**：明确区分“已提交待审批”“正在执行”“领域已生效”和 direct 止损完成；自提申请对 human 仍显示批准按钮。

这些动作把直接确认/二次确认改成统一审批主流程。接真前必须制作可点击 mock，至少覆盖身份确认、凭据轮换、信任授予、批量换区与立即止损的四态，经用户浏览器评审拍板后才能接真实审批 API。

## 7. 实施任务拆分

1. 运行 identity/credential/trust/topology/Lobby/调度/管理台相关测试基线并记录结果。
2. 测试先红：完整 operation 矩阵、V1/V2/MCP/内部入口覆盖、machine/system 禁批/禁 permit、direct 止损与恢复升级审批。
3. 测试先红：各 adapter 冻结/漂移、凭据明文只返一次、哈希只进领域 pending change 且不进审批/审计、批准原子应用/终态失效/worker 重试、批量目标 hash、领域事务/审计失败回滚。
4. 先完成五类代表流程的四态可点击 mockup与浏览器评审。
5. 最小实现四类 adapter 与 service permit 守卫，复用既有状态机、repository 和审计，不引新依赖或通用 credential 表。
6. 收敛旧 approve/rotate/grant/rezone/default-entry/Lobby/drain 入口并接统一申请/执行结果。
7. 独立复核从每个 handler/MCP/后台入口追到最终副作用，尝试旧路由、body 分支、system policy 和 service 直调旁路。
8. 重跑相关完整门禁；实施期同步 API、ARCHITECTURE、UX、OPERATIONS、SECURITY、旧领域 spec 与 CHANGELOG。

## 8. 测试与验收

### 8.1 自动化验收

1. 矩阵中每个 operation 恰有分类；所有扩权/恢复/迁移动作在批准前零领域副作用，所有 machine/system 无法批准或构造 permit。
2. identity approve/unbind/conflict/enable 只由批准 worker 执行；disable/reject 立即止损且不能顺带恢复或迁移。
3. credential create/rotate/enable 的明文仅能由申请 human 在审批成功后一次性兑换；哈希只存在对应领域的 pending change/活动凭据，不进入通用审批/审计/日志；批准原子应用同一 change，拒绝/撤回/过期使其永不生效，revoke 即时失效且重新启用走新审批。
4. trust grant/expand/regrant 走审批，revoke 直接强审计；撤销后旧批准不能复活信任。
5. assignment/rezone/default-entry/Lobby move/draining=false 走审批，draining=true 直接；目标漂移、互斥或活动任务冲突均 failed 且无半写。
6. V1/V2/REST/MCP/内部同义入口共享 operation/adapter；删除任一登记使覆盖测试失败，未知入口运行时 fail-closed。
7. 批量列表分页、全集 hash、查询计数无 N+1；事务、幂等与控制面重启恢复符合 FR-207。

### 8.2 真实环境验收

1. 分别用 human、full API key 与 MCP automation 提交危险申请；只有 human 能批准，本人申请可自批，批准后无需申请方再次 execute。
2. 完成一次身份确认、namespace token/API key 轮换、信任授予、服务器换区和 Lobby 迁移；每条链可从申请追到领域结果与审计。
3. 直接撤销凭据/信任、禁用身份、启用排空，确认立即止损；相反恢复动作全部要求新审批。
4. 在批准前修改目标或领域版本，旧申请失败且零副作用；重新申请后成功，不复用旧许可。
5. 四态 mockup 先经用户浏览器评审，接真后再完成真实页面验收。

## 9. 固定边界

- human 可以审批本人申请；API key、MCP、system 永远不能审批。
- 撤销凭据/信任、禁用/拒绝身份、启用排空属于 direct 止损；启用、重授、恢复调度和改变拓扑必须审批。
- 统一审批只授权既有领域状态转换，不取代领域状态机，也不产生第二次批准/启动。
- 凭据明文和哈希都不落通用审批真源；明文创建/轮换时只返回一次，哈希只存领域 pending change，批准只原子应用该引用。
