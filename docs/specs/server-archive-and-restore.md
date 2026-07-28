# 功能规格：server 归档与恢复

> 状态：草拟　·　关联 PRD：FR-215　·　分支：待执行时创建　·　依赖：FR-205、FR-206、FR-207、[ADR-0078](../adr/0078-stable-resource-identity-and-lifecycle-tombstones.md)、[ADR-0079](../adr/0079-principal-capability-and-dangerous-operation-approval.md)

## 1 背景与目标

身份 `disabled/unbound` 表达的是 Agent 凭据/绑定状态，不等于 server 资产退出运行。当前 server 又只能长期保留或被旧删除路径清理，无法安全表达“暂时停用、稍后按原身份与归属恢复”。

本 FR 为 server 增加独立 `active ↔ archived` 生命周期。归档保留 `serverId`、身份绑定、BC/大区/小区及 LobbyCluster 归属和历史，只改变有效运行资格；恢复复用原事实，不把 Agent 身份退回 pending。归档和恢复都通过 FR-207 的危险操作审批执行。

## 2 需求（要什么）

### 2.1 范围

- server 可申请归档；审批执行成功后从 active 变为 archived。
- archived server 可申请恢复；审批执行成功后回到 active。
- 归档期间保留稳定标识、显示名称、kind、identity 绑定、拓扑/大厅归属、排空和默认入口等事实，不物理删除任何行。
- 归档 server 不进入在线目录、健康/调度候选、广播/消息目标、交付目标、默认入口候选、BC 受管目录或 Agent 指令目标。
- 恢复不重新分配 serverId、不重新确认身份或归属；但恢复动作本身仍须取得一次 FR-207 `ExecutionPermit`。
- 列表与审计可查询 archived server，默认业务执行查询只返回“有效 active”资源。

### 2.2 非目标

- 不实现永久删除；`archived → tombstoned` 由 FR-217 承接。
- 不改变 AgentIdentity 自身状态机，也不自动把 disabled/unbound 身份变为 active。
- 不清空 BC/Zone/LobbyCluster 外键，不重置排空、默认入口或历史指标。
- 不给 env、BC、大区或小区增加独立生命周期。

## 3 设计（怎么做）

### 3.1 数据与状态机

server additive 增加：`lifecycle_status`（`active|archived`，默认 active）、`archived_at`、`archived_by`、`archive_reason`。FR-217 后续扩展同一状态列为 `tombstoned`，本 FR 不实现该转换。

```text
active ──审批执行 archive──▶ archived
   ▲                              │
   └────审批执行 restore──────────┘
```

- 申请、待审、拒绝、过期或执行失败都不改变 server 状态；审批状态只存在 FR-207 的 ApprovalRequest 中。
- 重复执行相同审批任务以审批任务幂等键收敛为同一结果；直接重复申请仍创建新的不可变申请。
- `effectiveActive = server.lifecycle_status == active AND namespace.effectiveActive`。身份、在线、健康、draining 等仍是下游各自资格条件，不能被生命周期覆盖。
- namespace 归档导致的有效停用不改 server 自身状态；namespace 恢复后，独立 archived server 仍为 archived。

### 3.2 影响快照与审批执行

operation 固定为 `server.archive`、`server.restore`，schemaVersion 均从 1 开始，requiredCapability 为 `approval.request`，classification 为 `approval_required`，riskLevel 为 `high`，分别登记独立冻结/执行 adapter。申请冻结：server 内部 id、namespace id/code、serverId、displayName、当前生命周期、identity 摘要、归属 id/code、在线/可调度状态、默认入口/排空事实、资源版本与规范化原因。

- human、api_key 或 MCP 可在具备申请 capability 时提交；只有 human 可批准，机器主体不能构造许可。
- 批准后由 FR-207 持久 worker 直接调用 server 生命周期 adapter，并携带不可外部构造的 `ExecutionPermit`；不得回调 REST。
- 执行前重新校验资源版本、当前状态和影响快照。server 已换区、身份绑定变化或生命周期变化时以 `approval_target_changed` 失败，要求重新申请，不自动扩大批准范围。
- 事务内同时写生命周期事实、审批执行结果所需领域证据和强审计；审计失败则业务事务回滚。

### 3.3 归档/恢复领域效果

归档成功后：

- 保留 AgentIdentity 行及当前绑定，Agent 以该绑定继续请求时返回明确的 `SERVER_ARCHIVED`，不得创建同 serverId 的新资产或 pending 身份。
- 活跃目录、健康视图、调度、广播、交付、默认入口、BC 同步与命令选择器统一调用生命周期资格判断；不能仅在 UI 隐藏。
- 在线连接在既有连接关闭/租约机制内收敛，不在审批请求线程做长时间阻塞等待。

恢复成功后：

- 只把 lifecycle 恢复为 active 并清空当前归档元数据；历史归档/恢复动作仍由 ApprovalRequest 与审计永久留存。
- 原 identity 若仍为 active 且绑定匹配，可直接重新进入正常鉴权；若原 identity 已 disabled/unbound/conflict，则仍按该状态拒绝，不由恢复越权修正。
- 原 BC/Zone/LobbyCluster 归属继续生效；若外部事实已漂移，执行前守卫失败并要求重新申请，不猜测新归属。

### 3.4 API 与错误

| 方法 | 路径 | 语义 |
|---|---|---|
| GET | `/admin/v2/servers/{id}/lifecycle-impact?action=archive|restore` | 生成当前脱敏影响预览，不产生副作用 |
| POST | `/admin/v2/approval-requests` | `{operationKey:"server.archive"|"server.restore", parameters:{serverRowId:id}, reason}` + `Idempotency-Key`；由对应 adapter 冻结并创建统一申请 |
| GET | `/admin/v2/servers` | 增加 `lifecycleStatus=active|archived|all`；管理页默认 active，明确切换后可看 archived |

创建申请遵循 FR-207：返回 `202`、`Location` 与统一安全视图，不另造领域审批表/API。常见领域错误：404 `server_not_found`；409 `server_not_active`、`server_not_archived`、`approval_target_changed`；403 `capability_denied`；422 `reason_required`。旧 `/admin/v1` server 删除/停用同义入口必须移除或接入同一 operation 分类，不能绕过审批。

## 4 UX / 交互

用户从 `/servers` 行操作或详情抽屉进入。主循环为：查看状态与影响 → 填原因 → 提交审批 → 跳转/侧栏查看审批进度 → 成功后刷新列表。

- active 行提供“申请归档”；archived 行提供“申请恢复”和 FR-217 的永久删除入口。按钮显示目标 serverId，避免重名误选。
- 确认层展示身份绑定是否保留、当前归属、在线/默认入口影响及“归档不等于永久删除”。恢复层明确“不会重新审批身份，但本次恢复操作需审批”。
- 状态同时展示“自身 archived”与“因 namespace 归档而有效停用”，不可混成一个标签。
- **空态**：所选筛选无 server 时给出清除筛选入口；archived 为空时说明尚无归档资产。
- **加载态**：列表与影响预览分别显示骨架，未拿到影响快照前禁止提交。
- **错误态**：申请失败、快照漂移或审批执行失败显示可重提原因；不得假装已归档/恢复。
- **超大量态**：列表服务端分页，生命周期筛选下推；影响预览只返回该 server 的有界关联摘要。

这是既有页面的行操作与筛选补齐，可不制作独立全页 mockup；实现前用组件测试锁定确认层和四态。

## 5 任务拆分

- [ ] 先运行 server/identity/健康/调度/消息/交付/BC 目录与 web 相关测试基线。
- [ ] 先写失败测试：状态转换、审批许可缺失、影响漂移、归属/绑定保留、恢复不重审身份、父 namespace 有效停用。
- [ ] 最小实现 lifecycle 字段、repository 查询谓词与领域 adapter；不顺带重构身份状态机。
- [ ] 把有效资格接入在线目录、调度、广播、交付、默认入口、BC 同步和 Agent 指令边界，并补跨域集成测试。
- [ ] 接入 FR-207 operation/ExecutionPermit，增加影响预览、申请 API、contracts 与 `/servers` 交互。
- [ ] 独立复核所有旁路、旧 V1 同义入口、事务/审计原子性、重启恢复与 N+1 风险。
- [ ] 重跑完整相关门禁；同步 PRD/API/ARCHITECTURE/CHANGELOG 由主任务统一收口。

## 6 验收标准

### 6.1 自动化验收

1. 无有效 ExecutionPermit 不能调用归档/恢复领域方法；机器主体不能批准申请。
2. 归档成功只改变生命周期及归档元数据，serverId、displayName、identity 绑定和所有归属字段不变。
3. archived server 被所有运行目标解析器排除，直接鉴权/指令访问 fail-closed；管理列表用显式筛选仍可查。
4. 恢复后 active identity 无需进入 pending 或重新分配即可使用；disabled/unbound/conflict 身份不会被错误启用。
5. namespace 归档/恢复不覆写 server 自身状态；独立 archived server 始终保持 archived。
6. 快照漂移、重复 worker、事务失败和进程重启分别得到幂等、可恢复且可审计的终态。
7. 列表保持分页，跨域资格检查无循环内查询或远程调用。

### 6.2 真实验收

1. 在真实控制面选一台具备 active 身份和既有 Zone/Lobby 归属的测试 server，提交并批准归档；Agent 被拒绝进入运行目录，调度/广播/交付均不再选中它，历史仍可查。
2. 提交并批准恢复；同一 serverId 与 identity 重新连接，无身份重批或归属重配，原 disabled 身份不会被越权启用。
3. 归档/恢复审批从申请、批准、worker 执行到结果均可在审批中心与审计中追溯；页面四态可操作。

## 7 风险 / 待定

- 最大风险是只在某个列表过滤 archived，留下广播、交付或 Agent 鉴权旁路；实现必须复用单一资格谓词并做消费者清单测试。
- 恢复“不重新审批”只指不重新审批 Agent 身份/归属；根据 ADR-0078，本次 `server.restore` 操作本身仍走通用审批。
- 在线连接如何快速断开沿用现有连接/租约机制；本 FR 不引入阻塞等待或新的分布式组件。
- 已决定：归档保留稳定身份和归属；恢复后沿用原事实；无物理删除；永久删除另由 FR-217 承接。
