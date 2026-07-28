# 功能规格：BC 受管目录立即重同步（FR-201）

> 状态：草拟　·　关联 PRD：FR-201　·　分支：待执行时创建

## 1. 背景与目标

FR-200 建立 BC 对当前 namespace 全部受管 Bukkit 的目录同步，以及玩家首次落脚所需的大厅候选快照。正常情况下，BC 通过自动同步收敛；但发布变更、故障恢复和现场排查时，运维还需要一个不等待下一轮周期的明确入口，立即要求在线 BC 重新取得控制面权威快照并原子应用。

本功能提供两种后台操作：对一个 namespace 下全部在线 BC 执行全量重同步，或只对一台在线 BC 定点重试。两种操作使用同一条 Agent 命令契约，执行结果进入既有命令生命周期、命令观测和审计链路；离线 BC 明确拒绝且不创建命令。

目标：

- 让运维能从管理台立即收敛一台或一组 BC 的受管服务器目录与大厅候选快照。
- 保持自动同步为常态恢复机制；立即重同步只负责在线诊断与人工加速，不形成离线任务队列。
- 复用既有命令队列的持久状态、单飞执行、过期清理和观测能力，同时与 `resync-config` 的配置/文件重同步严格分离。

## 2. 需求（要什么）

### 2.1 范围内

- namespace 级立即重同步：选定一个 namespace，对其全部**在线 BC**分别创建目录重同步命令；离线 BC 作为拒绝结果返回，不为其创建命令。
- 单 BC 立即重同步：只允许选择一台属于目标 namespace、角色为 BC 且当前在线的 Agent。
- 命令执行时重建两份同源快照：Beacon 受管 Bukkit 目录，以及 LobbyCluster 的大厅候选快照。
- 成功应用后命令进入 `done`；拉取、校验、应用或缓存持久化失败时进入 `failed`，保留脱敏的中文原因摘要。
- 同一 BC 同时最多执行一条目录重同步命令；并发重复请求被明确拒绝，不产生并行拉取或重复应用。
- 管理台展示本次接受数、拒绝数、逐目标原因，并可查看 `pending / fetched / done / failed / expired` 状态。
- namespace 与单 BC 操作都写审计，记录作用域、目标、命令 ID 和接受/拒绝摘要，不记录 token、地址或完整目录内容。

### 2.2 明确不做

- 不复用或改义既有 `resync-config`。它仍只重拉有效配置、文件树与覆盖集；本功能使用独立命令类型 `bc-directory-resync`。
- 不把命令排队给离线 BC，不设置离线等待 TTL，不在 BC 重连后补执行旧命令；重连后的恢复由 FR-200 自动全量同步负责。
- 不提供按大区、小区或单台 Bukkit 增量推送；BC 的受管范围恒为整个 namespace。
- 不改变 FR-200 的目录选择、手工服务器保护、LobbyCluster 候选过滤、快照校验或 fail-static 语义。
- 不新增第二套命令队列、消息中间件或后台任务模型，不引入第三方依赖。
- 不负责玩家选区、排队、区服内默认服务器或后续切服调度。

## 3. 设计（怎么做）

### 3.1 依赖与总体流程

本功能依赖 FR-200 已交付的“拉取权威目录与大厅快照并原子应用”入口，只增加管理面触发与命令编排：

```text
管理员触发
  → 控制面解析 namespace / BC 目标并读取一次在线注册表快照
  → 每个目标独立校验角色、namespace、在线态和单活跃命令约束
  → 对通过者创建 pending 的 bc-directory-resync 命令并唤醒 Agent
  → BC 拉取命令，CAS pending → fetched
  → 调用 FR-200 同一同步入口，拉取、校验、原子应用并落最后有效缓存
  → 回传结果，CAS fetched → done / failed
  → 管理台从命令状态与审计读取结果
```

控制面只负责下发和记录，不在命令载荷内复制目录。命令载荷固定为空对象 `{}`；BC 执行时从 FR-200 权威端点取得当前最新快照，避免排队期间拓扑变化导致旧载荷覆盖新状态。

### 3.2 管理 API 契约

管理面沿用 `/admin/v2/*` 的登录令牌/API 密钥鉴权和统一错误响应 `{code, message, traceId}`。两个 POST 都是写操作，仅 `full` 角色可用，`readonly` 返回 `403 FORBIDDEN`。

| 方法 | 路径 | 请求 | 成功响应 |
|---|---|---|---|
| POST | `/admin/v2/namespaces/{id}/bc-directory-resyncs` | 无 body | `202`，`{namespaceId, requested, accepted, rejected, results:[{serverId, accepted, commandId?, status?, code?, message?}]}` |
| POST | `/admin/v2/servers/{id}/bc-directory-resyncs` | 无 body；`id` 为 server 数字主键 | `202`，`{namespaceId, serverId, accepted:true, commandId, status:"pending"}` |

namespace 级写操作不分页：一次点击的语义就是对触发时刻注册表快照内的全部 BC 求值，不能由 `page` 把一次操作静默拆成不完整作用域。其命令历史与后续状态查询复用 `GET /admin/v1/commands?namespace=&serverId=&type=bc-directory-resync&status=&page=&size=`，`page` 从 1 起、`size` 缺省 20 且上限 200；响应投影不得含 `payload` 或其他瞬态内容。

namespace 级处理规则：

1. 先验证 namespace 存在，再读取一次该 namespace 的 BC 资产与在线注册表快照。
2. 每个 BC 独立处理，不采用“一个失败整批回滚”：在线且无活跃同类命令者创建命令；离线、角色异常或已有活跃命令者只返回拒绝项。
3. 至少一台目标接受时返回 `202`；全部目标均被拒绝时返回 `409 NO_ELIGIBLE_BC`，错误 detail 仍带逐目标拒绝原因。
4. namespace 下不存在任何 BC 资产时返回 `409 NO_BC_TARGETS`。

错误码：

| HTTP | code | 场景 |
|---|---|---|
| 400 | `INVALID_PARAM` | 路径 ID 非法或请求形状错误 |
| 403 | `FORBIDDEN` | readonly 角色或无写权限 |
| 404 | `NAMESPACE_NOT_FOUND` | namespace 不存在 |
| 404 | `SERVER_NOT_FOUND` | 单 BC 目标不存在 |
| 409 | `TARGET_NOT_BC` | 单目标不是 BC；不创建命令 |
| 409 | `BC_NAMESPACE_MISMATCH` | 目标与解析出的 namespace 关系不一致；不创建命令 |
| 409 | `BC_OFFLINE` | 单目标当前离线；不创建命令 |
| 409 | `BC_DIRECTORY_RESYNC_ACTIVE` | 该 BC 已有 `pending` 或 `fetched` 的同类命令；detail 返回活跃 commandId/status |
| 409 | `NO_BC_TARGETS` | namespace 下没有 BC 资产 |
| 409 | `NO_ELIGIBLE_BC` | namespace 下有 BC，但本次没有任何目标可接受命令 |

### 3.3 命令契约与并发

- 新命令类型：`bc-directory-resync`。它与 `resync-config` 并列，加入控制面及 Agent 的命令类型白名单、命令观测类型标签和中文显示映射。
- 生命周期只允许 `pending → fetched → done / failed`，或由既有过期清理把陈旧 `pending / fetched` 转为 `expired`；`ready` 对本命令非法。
- 控制面创建命令与写请求审计在同一事务内完成，事务提交后才发 `command-pending` 唤醒；唤醒失败不回滚已持久化命令，Agent 可由既有命令长轮询拉取。
- 同一 `(namespace, serverId, type=bc-directory-resync)` 同时最多一条 `pending / fetched`。创建路径必须在项目支持的单控制面进程内串行化“查活跃命令 + 建命令”，并用并发测试证明多请求只有一条成功，其余得到 `BC_DIRECTORY_RESYNC_ACTIVE`。
- namespace 级触发按目标独立事务创建，避免一台 BC 的并发冲突阻断其他在线 BC；返回顺序按 `serverId` 升序，便于稳定展示和测试。
- 重复执行的结果必须幂等：前一条命令终态后可再次创建；若目录和大厅快照未变化，FR-200 应用入口合法 no-op，命令仍为 `done`。

Agent 回执复用 `POST /beacon/v1/agent/commands/result`，请求 `{commandId, ok, reason}`。服务端除校验命令存在、类型正确且当前为 `fetched` 外，还必须校验该命令的 namespace/serverId 与当前认证 Agent 一致；越权或类型不符统一按不存在处理为 `404 COMMAND_NOT_FOUND`，不得泄露其他目标的命令信息。`reason` 只接受脱敏中文摘要，控制面统一去控制字符并截断到 512 字符。

### 3.4 BC Agent 执行机制

- 命令执行器为 `bc-directory-resync` 增加独立分支，调用 FR-200 暴露的单一同步入口；不得转调 `forceResyncNow()`、`forcePollNow()` 或文件树 `resync`。
- 同步入口复用 FR-200 的串行门；周期同步与立即重同步并发时只允许一个应用过程在飞，后到者合并为一次后续检查或等待当前结果，不能交错修改 Bungee 目录。
- 每次执行先完整拉取并校验目录与 LobbyCluster 快照，再原子替换内存快照和 Beacon 受管的 Bungee 注册项；手工非 Beacon 服务器不覆盖、不删除。
- 只有新快照已成功应用并写入最后有效本地缓存后才回传 `ok=true`。任一步失败都保留原目录、原大厅候选和原缓存，回传 `ok=false` 与脱敏中文原因。
- Bukkit Agent 不支持此命令；控制面角色校验应保证不会下发。若因数据漂移收到，Agent 必须回传 `failed` 和“当前 Agent 角色不支持 BC 目录重同步”，不能静默忽略导致命令悬挂。
- 执行不得阻塞 Bungee 主线程；日志使用中文正式日志，开始/成功用 INFO，保留旧快照的执行失败用 WARN，程序不变量被破坏才用 ERROR，并包含 commandId/serverId，不输出 token 或完整目录。

### 3.5 审计与状态可见性

- 单 BC 操作写 `instance.bc-directory-resync`，目标为该 serverId，detail 只含 `commandId`、namespace、触发作用域。
- namespace 操作写一条 `namespace.bc-directory-resync`，detail 只含 namespace、requested/accepted/rejected 计数和已接受 commandId 列表；离线目标仅记录 serverId 与 `BC_OFFLINE`，不记录地址。
- 命令最终状态、脱敏失败摘要与耗时继续由 `agent_command` 和命令观测页负责，不复制进审计 detail，保持单一真源。
- `/lobby-clusters` 的操作结果可按本次返回的 commandId 展示即时状态；刷新页面后的历史查询进入既有 `/commands` 页面并按 namespace、serverId、type 过滤。

## 4. UX / 交互

- 用户任务：集群运维人员在目录未及时收敛或排障时，对整个 namespace 或单台 BC 立即重建受管目录与大厅快照，并看到每个目标是否接受及最终结果。
- 进入路径：`集群 → 大厅集群（/lobby-clusters）`；namespace 工具栏提供“全部 BC 立即重同步”，BC 列表行提供“重同步此 BC”。
- 操作闭环：点击触发 → 按钮进入提交态防重复点击 → 展示接受/拒绝汇总 → 展开逐目标状态与原因 → 可跳转命令观测查看历史。
- 状态设计：
  - 空态：namespace 无 BC 时禁用 namespace 操作并说明“当前 namespace 没有 BC”。
  - 加载：提交期间只禁用相同作用域按钮，不阻塞页面其他只读信息。
  - 部分成功：同时展示“已接受 N 台、拒绝 M 台”，拒绝项逐台显示中文原因；不能用一个成功 toast 掩盖离线目标。
  - 全失败：保留后端 `traceId`，展示可操作原因，不伪装为已下发。
  - 超大量：目标结果按 serverId 稳定排序并在前端虚拟/折叠展示；命令历史使用服务端分页，不一次拉全量历史。
- IA 挂载与 mockup：本功能只在 FR-199 新页面内增加操作，不另建页面；必须等待 FR-199 `/lobby-clusters` 可点击 mockup 通过后再接真，若操作改变该页已确认主流程则随 FR-199 一并复审。

## 5. 与其他规格的边界

| 对方 | 本功能依赖 | 本功能交付 |
|---|---|---|
| [lobby-cluster-authority.md](lobby-cluster-authority.md)（FR-199） | namespace、LobbyCluster、BC/Bukkit 资产与 `/lobby-clusters` 页面 | 页面上的 namespace/单 BC 操作和状态入口 |
| [bc-namespace-directory-and-lobby-entry.md](bc-namespace-directory-and-lobby-entry.md)（FR-200） | 权威目录端点、快照校验、原子应用、本地缓存、受管项保护 | 人工触发同一同步入口，不改变其语义 |
| [command-observability.md](command-observability.md) | `agent_command` 生命周期、分页观测与敏感字段投影保护 | 新命令类型、中文标签和可过滤状态 |
| [server-row-quick-actions.md](server-row-quick-actions.md) | 命令队列、Agent 拉取/回执模式 | 保持 `resync-config` 原语义，不复用其执行入口 |
| [ADR-0027](../adr/0027-reverse-fetch-channel-and-security.md) | 持久命令队列、拉取、CAS 与唤醒模式 | 新类型沿用既有模式，不产生新的命令通道架构决策 |

本功能本身不新增 ADR；FR-200 对 BC 目录与大厅落脚权威的 ADR 是前置决策真源。若实现发现必须改变命令队列安全边界或离线行为，应先停下并另提 ADR，不得在本 spec 内静默扩张。

## 6. 任务拆分

- [ ] 基线：运行控制面命令服务/handler/观测相关测试、Agent 命令执行器测试、Web 相关测试与构建，确认修改前全绿；记录真实命令状态与 API 现状。
- [ ] 红测（控制面）：先覆盖新命令类型、单 BC 在线/离线/错角色、namespace 部分成功/全失败、readonly 403、命令与审计同事务、回执目标校验、同 BC 并发单活跃和分页观测过滤，确认测试先失败。
- [ ] 最小实现（控制面）：增加 `bc-directory-resync` 白名单、请求服务、两个 Admin API、Agent 回执分派、审计动作、路由写操作覆盖和命令观测中文映射，不改 `resync-config`。
- [ ] 红测（Agent）：先覆盖新命令调用 FR-200 同步入口、成功/失败回执、旧快照保留、Bukkit 错角色失败、周期同步并发单飞和主线程不阻塞，确认测试先失败。
- [ ] 最小实现（Agent）：接入独立命令分支与同步端口，复用 FR-200 的校验/原子应用/缓存，不复制第二套目录同步逻辑。
- [ ] 红测与最小实现（Web）：覆盖 namespace/单 BC 触发、部分成功、全失败、按钮防重复、命令状态与历史跳转；只在 FR-199 已评审页面上接线。
- [ ] 相关门禁：控制面全量 Go 测试与静态检查、Agent 全量 Gradle 构建/测试（严禁执行 `gradle --stop`）、Web 测试与生产构建全部通过。
- [ ] 文档同步：更新 PRD 状态、ARCHITECTURE、API、UX、命令类型/审计说明与 CHANGELOG；不修改已接受 ADR 正文。
- [ ] 独立复核：审查 `git diff`，确认没有离线建命令、没有把目录塞入命令载荷、没有复用 `resync-config`、没有敏感失败原因或无关改动。

## 7. 验收标准

### 7.1 自动化验收

1. 单 BC 在线时，Admin API 返回 `202` 和一条 `pending` 的 `bc-directory-resync`；命令、审计同事务提交，Agent 收取后严格走 `fetched → done/failed`。
2. 单 BC 离线返回 `409 BC_OFFLINE`，数据库无新增命令；namespace 操作中的离线 BC 返回拒绝项且同样无命令。
3. namespace 同时含在线、离线和已有活跃命令的 BC 时，只为合格在线目标创建命令，响应准确给出接受/拒绝计数与逐台原因；全部不合格返回 `409 NO_ELIGIBLE_BC`。
4. readonly 调用两个写端点均为 403；未知 namespace/server、非 BC、namespace 漂移和非法参数分别按 §3.2 错误码返回。
5. 32 个并发请求命中同一 BC 时最多一条进入 `pending`，其余返回 `BC_DIRECTORY_RESYNC_ACTIVE`；周期同步与命令同步并发时也只有一个目录应用过程。
6. Agent 成功路径同时收敛受管 Bukkit 目录与大厅候选快照；已是最新时合法 no-op 仍回 `done`；手工非 Beacon 服务器保持不变。
7. 拉取、校验、应用或缓存失败时命令为 `failed`，原内存目录、原大厅候选与最后有效缓存不变；失败摘要已脱敏、无控制字符且不超过 512 字符。
8. 命令历史可按 `type=bc-directory-resync`、namespace、serverId、status 服务端分页查询，响应不含 payload/完整目录/token；`ready` 不会出现在该类型状态流中。
9. `resync-config` 的既有自动化测试不变且全绿，证明两种重同步没有串义。

### 7.2 真实 BC 验收（RC 阻断）

在已确认的真实环境（至少 1 个 BC、2 个大厅服、1 个普通区服）执行：

1. 分别触发单 BC 与 namespace 重同步，命令观测能看到 `pending → fetched → done`，BC 目录和大厅候选与控制面当前事实一致。
2. 修改 LobbyCluster 成员或普通区服归属后立即重同步，不等待周期即可收敛；重复触发终态命令结果不改变最终目录。
3. 断开一台 BC 后触发单 BC 操作，管理台明确显示离线且没有命令；BC 重连后由 FR-200 自动同步恢复，不执行断线期间的旧操作。
4. 制造一次可恢复的拉取/校验失败，命令进入 `failed`、管理台显示脱敏中文原因，BC 继续使用最后有效目录和大厅快照。
5. namespace 若同时有在线与离线 BC，页面明确显示部分成功，不把离线目标伪装成已下发。

以上任一项缺少当前证据，FR-201 不得标记交付，首个 `v1.0.0-rc.N` 不得放行。

## 8. 风险 / 已拍板决定

### 8.1 已拍板

1. 同时支持 namespace 全量与单 BC 定点重试。
2. 只对在线 BC 创建命令；离线明确失败、绝不排队，重连依赖自动全量同步。
3. 重同步同时重建受管目录与大厅候选快照。
4. `bc-directory-resync` 与 `resync-config` 是两个独立命令类型和执行入口。
5. 重复终态执行允许合法 no-op；同一 BC 的进行中命令必须单飞。

### 8.2 风险

- namespace 操作允许部分成功，前端和审计必须保留逐目标失败语义；若只显示总成功 toast，会造成运维误判。
- 在线校验与命令拉取之间存在自然竞态：目标在建命令后离线时，命令按既有 TTL 进入 `expired`，不能反向把“创建时在线”改写成离线排队语义。
- 本功能依赖 FR-200 提供唯一同步入口；若实现阶段发现只能复制目录应用逻辑，应先修正 FR-200 的职责边界，不能复制粘贴形成双实现。
