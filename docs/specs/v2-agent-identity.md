# 规格：Agent 身份与注册绑定（第二版）

> 状态：已实现（P1 基础闭环） · 关联 FR：FR-139, FR-140, FR-141 · 阶段：P1（0.21.x）

## 1. 背景与目标

Legacy 时期 agent 仅靠配置文件里的 serverId 自证身份，运维误改 serverId、复制整服目录都会静默污染区数据。第二版引入 agent 首启生成并持久化的唯一 `identityId`：serverId 只有与 identityId 完成绑定并经人工确认后才可信。本规格定义身份文件、首启生成、注册绑定、待确认/确认/拒绝/过期、身份冲突识别与处置、解绑/换区/禁用/重新绑定的完整状态机，以及配套的数据模型、API 契约与审计要求。

目标（对应 PRD §1.2 与 §3 核心约束）：

- agent 只靠 Beacon 地址、namespace 接入 token、serverId 即可自连接；身份由 agent 自管，不依赖 CoreLib。
- 首次接入必须人工确认；未确认、未分配区服前不可调度。
- 换区、换 serverId、解绑、禁用、冲突处置都必须在后台可见、可操作、全部入审计。
- 故障换机（同 serverId 新 IP）平滑恢复，不被误杀；复制目录导致的重复身份能被识别并冻结。

## 2. 范围

**做什么**：

- 身份文件格式、存放路径、首启生成与持久化、损坏处理（FR-139）。
- 注册绑定流程：`identityId + namespace + serverId` 三元绑定；待确认 → 确认 / 拒绝 / 过期闭环（FR-140）。
- 重复身份识别（bootId 机制）、四类冲突象限的判定与处置；解绑 / 换区 / 禁用 / 启用 / 重新绑定状态机（FR-141）。
- `agent_identity` 表结构细化；agent 面与管理面身份域 API 契约；全部操作入审计。

**明确不做**：

- 心跳、指标采样、健康值、schedulable 判定 → `v2-metrics-health-scheduling.md`（P4）。本规格只输出「身份状态」这一 schedulable 的前置输入。活性（在线 / 失联）判定同归彼处权威：v2 不设独立心跳，指标批上报兼作活性信号（>30s 判 lost）；本文的 pending 注册过期 TTL 是待确认清理机制，与活性无关。
- zone / region / server 实体权威定义、未分配 agent 的批量区服分配 → `v2-zone-authority.md`。
- namespace 与接入 token 的管理、互通信任 → `v2-namespace-isolation.md`。本规格只消费「token↔namespace 校验通过」这一前置事实。
- 审计表结构（沿用通用审计机制，本规格只定义必须写入的审计事件）。
- 身份文件加密、agent 双向 TLS 等安全增强（PRD 非目标，不做）。

## 3. 数据模型

共享实体（`namespace`、`server`）以基座 §3 与 `v2-zone-authority.md` 为权威，此处不复制。本域权威表如下。

### 3.1 `agent_identity`

一个 identityId 对应**一行**（`identity_id` 唯一索引），记录该身份的当前绑定与状态；绑定变更历史不建独立历史表，靠审计流水追溯（默认决定，见 §8-5）。

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | BIGINT，主键，自增（GORM 抽象） | 内部主键 |
| `identity_id` | VARCHAR(64)，非空 | agent 首启生成的 UUIDv4，小写；唯一索引（列形态与 `v2-zone-authority.md` §3.7 权威定义一致） |
| `namespace_id` | BIGINT，非空 | 绑定的 namespace；索引 |
| `server_id` | VARCHAR(64)，非空 | agent 上报的服务器标识（ns 内业务唯一，见 3.2） |
| `kind` | VARCHAR(16)，非空 | `proxy` / `backend`，应用层校验 |
| `status` | VARCHAR(16)，非空 | 状态机取值见 §4.3，应用层校验（禁 ENUM） |
| `boot_id` | VARCHAR(36)，可空 | 最近一次注册上报的进程启动标识 |
| `last_addr` | VARCHAR(64)，可空 | 最近一次注册/请求的来源地址（host:port） |
| `agent_version` | VARCHAR(32)，可空 | agent 版本号，注册时上报 |
| `pending_expires_at` | DATETIME，可空 | pending 状态的过期时刻（UTC）；非 pending 时为 NULL |
| `bound_at` | DATETIME，可空 | 最近一次确认（approve）时刻（UTC） |
| `status_changed_at` | DATETIME，非空 | 最近一次状态变更时刻（UTC） |
| `conflict_reason` | VARCHAR(255)，可空 | 进入 conflict 时的机器可读原因（如 `duplicate-boot-id`） |
| `created_at` / `updated_at` | DATETIME，非空 | GORM 标准时间戳 |

索引：

- `uk_identity_id`：`identity_id` 唯一索引。
- `idx_ns_server`：`(namespace_id, server_id)` 普通复合索引（非唯一，原因见 3.2）。
- `idx_status`：`status` 普通索引（待确认列表、过期扫描）。

### 3.2 serverId 占用的唯一性

「同一 namespace 内，处于**活跃态**（`pending` / `active` / `disabled` / `conflict`）的 `server_id` 不得重复」由应用层在 DB 事务内校验（注册、确认、冲突处置路径都要过此校验）。不用 DB 唯一索引：终结态（`unbound` / `rejected` / `expired`）的历史行允许保留相同 `server_id`，而 MySQL 不支持部分唯一索引，为保持可移植性不引入方言特性。

### 3.3 与 `server` 实体的关系

- `agent_identity` 是「身份绑定事实」，`server`（基座 §3）是「拓扑资产」。**approve 成功时**：若 `(namespace_id, server_id)` 对应的 `server` 行不存在则创建（归属字段为空即未分配：backend 的 `zone_id`、proxy 的 `bc_cluster_id` 均为 NULL——server 按 kind 双挂，见 `v2-zone-authority.md` §3.6），存在则复用。同一事务内完成。
- 解绑 / 禁用不删除 `server` 行；`server` 的展示应能关联出当前绑定身份的状态（无 active 绑定 = 该服无可信 agent）。
- 注册、活性等运行态真源仍在 Go 进程内存（架构不变量 §3）；`agent_identity` 只落低频绑定事实与状态，不落活性（v2 无独立心跳，活性 = 指标批上报兼任，权威归 `v2-metrics-health-scheduling.md` §4.2）。

## 4. 机制与状态机

### 4.1 身份文件（FR-139）

**路径**：agent 插件数据目录内固定文件 `identity.yml`。Bukkit/Paper 侧为 `plugins/Beacon/identity.yml`，BC 侧同构（各自平台的插件数据目录）。不放全局路径，随服目录走——这正是「复制目录会复制身份」需要检测的原因，也是身份与该服数据强关联的保证。

**格式**（YAML，kebab-case，含中文注释，遵守 `config-files.md`）：

```yaml
# Beacon agent 身份文件。首次启动自动生成，唯一标识本服务器实例。
# 严禁手工修改或复制到其他服务器目录；换机迁移时须随数据目录整体迁移。
# 身份文件格式版本
format-version: 1
# 本 agent 的唯一身份标识（UUIDv4，首启生成后终身不变）
identity-id: "3f2a1b7c-9d4e-4a6b-8c1d-2e5f7a9b0c3d"
# 生成时刻（UTC）
created-at: "2026-07-06T08:00:00Z"
```

**首启生成与持久化**：

1. agent 启动时读取 `identity.yml`；不存在 → 生成 UUIDv4（小写）、填充三字段、**原子写入**（先写临时文件再 rename），日志 INFO 记录「首启生成身份」。
2. 文件存在且合法 → 直接使用，重启不变（FR-139 验收）。
3. 文件存在但**损坏**（YAML 解析失败 / `identity-id` 非法 UUID / `format-version` 不识别）→ **不自动重生成**（重生成等于伪造新身份，会造成绑定漂移）。agent 打 ERROR 日志提示运维人工处理（恢复备份或删除文件按新身份重新走确认流程），本次启动**不发起注册**，其余本地能力按 fail-static 快照继续（不阻断玩家进服）。默认决定，见 §8-3。
4. 文件 IO 在 TabooLib async 线程完成，不阻塞 MC 主线程（架构不变量 §5）。

**bootId**：agent 每次进程启动生成一个随机 UUIDv4（仅存内存、不落盘），随注册请求上报。用于区分「同一身份的先后进程」（正常重启/换机）与「同一身份的并发进程」（复制目录），见 §4.5。

### 4.2 注册绑定流程（FR-140）

前置：agent 配置里有 Beacon 地址、namespace、namespace 级接入 token、serverId（基座 §2）。请求携带 `X-Beacon-Token`，token↔namespace 校验由 namespace 域中间件完成，失败即 401，不进入本流程。

```
agent 启动
  └─ 读/生成 identity.yml
  └─ POST /beacon/v2/agent/register {identityId, serverId, kind, bootId, agentVersion, addr}
       ├─ identity 无记录            → 建行 status=pending，返回 202 {status:"pending"}
       ├─ status=pending（同三元组） → 刷新 bootId/addr/过期时间，返回 202 pending
       ├─ status=expired（同身份）   → 回 pending（重新申请），返回 202 pending
       ├─ status=unbound（同身份）   → 回 pending（重新绑定，可换 serverId/namespace），返回 202
       ├─ status=active 且三元组一致 → 更新 bootId/addr，返回 200 {status:"active"}（重启/换机恢复）
       ├─ status=disabled 且三元组一致 → 更新 bootId/addr，返回 200 {status:"disabled"}
       ├─ status=rejected            → 403 {code:"identity_rejected"}（需后台先「允许重新申请」）
       ├─ status=conflict            → 409 {status:"conflict"}（等待后台处置）
       └─ 三元组不一致的各种情况     → 按 §4.4 冲突象限处理
  └─ 确认前循环：GET /beacon/v2/agent/registration（长轮询）直到 status 变化
       ├─ → active：进入正常运行（指标上报兼活性信号，归 P4 域）
       ├─ → rejected/expired：停止轮询，日志 WARN，退避后可重新 register（expired 可自动重试；rejected 不自动重试）
       └─ 超时（无变化）：返回 304，agent 立即续轮询
```

规则：

- **人工确认前，agent 只能调 `register` 与 `registration` 两个端点**（基座 §2）；其余 agent 面请求一律 403。
- 注册成功绑定（active）后，agent 所有 v2 请求另带 `X-Beacon-Identity: <identityId>`；服务端身份中间件校验 identityId 存在、status ∈ {active, disabled}、与 token 的 namespace 匹配，并比对内存注册表中的 bootId（不匹配触发 §4.5 冲突检测）。
- **pending 过期**：`pending_expires_at = 进入 pending 时刻 + registration-pending-ttl`（运维设置，默认 72h，热更生效，见 §8-2）。控制面周期扫描（复用后台任务框架，扫描间隔 1 分钟级），超期行转 `expired` 并写审计（操作者 = system）。agent 长轮询会收到 expired，重新 register 即回 pending——过期不是惩罚，只是清理待办列表。
- 注册与状态变更全部在 DB 事务内完成；serverId 占用校验（§3.2）在同事务内做。

### 4.3 身份状态机（FR-140/141）

状态取值（`agent_identity.status`，VARCHAR + 应用层校验）：

| 状态 | 含义 | 可调度输入* |
|---|---|---|
| `pending` | 已注册待人工确认 | 否 |
| `active` | 已确认，绑定生效 | 是（还需 P4 其余条件） |
| `rejected` | 被人工拒绝，agent 再注册被 403 | 否 |
| `expired` | 待确认超期作废，agent 可重新注册 | 否 |
| `disabled` | 被人工禁用：保留绑定、可注册可观测，但不可调度、不接收指令 | 否 |
| `conflict` | 检出重复身份/冲突，冻结待人工处置 | 否 |
| `unbound` | 已解绑，绑定关系终止；同身份可重新注册开启新绑定 | 否 |

\* 「可调度输入」指身份域向 P4 schedulable 判定提供的必要条件之一，非充分条件（还有未分配、排空、不健康等，归 P4）。

**状态迁移表**（未列出的迁移一律非法，代码里显式拒绝并报错）：

| # | 当前状态 | 事件 | 触发者 | 下一状态 | 附加动作 |
|---|---|---|---|---|---|
| T1 | （无记录） | agent 注册 | agent | `pending` | 建行，设 `pending_expires_at`；审计 |
| T2 | `pending` | agent 重复注册（同三元组） | agent | `pending` | 刷新 bootId/addr/过期时间；不重复审计 |
| T3 | `pending` | 确认（approve） | 管理员 | `active` | 创建/关联 `server` 行（§3.3）；设 `bound_at`；换区重确认时同事务按确认目标落区并清预填（见下「换区」）；审计 |
| T4 | `pending` | 拒绝（reject，原因必填） | 管理员 | `rejected` | 审计 |
| T5 | `pending` | 超期扫描 | 系统 | `expired` | 审计（操作者 system） |
| T6 | `expired` | agent 注册 | agent | `pending` | 重设过期时间；审计 |
| T7 | `rejected` | 允许重新申请（allow-reapply） | 管理员 | `expired` | 审计；此后 agent 注册走 T6 |
| T8 | `active` | 禁用（disable，原因必填） | 管理员 | `disabled` | 审计 |
| T9 | `disabled` | 启用（enable） | 管理员 | `active` | 审计 |
| T10 | `active` / `disabled` | 解绑（unbind，原因必填） | 管理员 | `unbound` | 审计；内存注册表摘除该实例；换区工单发起时由换区事务复用本迁移（见下「换区」） |
| T11 | `unbound` | agent 注册（可换 serverId / namespace） | agent | `pending` | 行重用：更新三元组、重走确认；审计 |
| T12 | `active` | 冲突检出（§4.5） | 系统 | `conflict` | 写 `conflict_reason`；审计（system）+ 触发告警 |
| T13 | `conflict` | 冲突处置：保留指定实例（resolve） | 管理员 | `active` | 以指定 bootId 为准更新绑定；对落败实例后续请求返回 409；审计 |
| T14 | `conflict` | 冲突处置：解绑 | 管理员 | `unbound` | 同 T10；审计 |

**换区**：按 PRD §3 字面语义（2026-07-07 拍板，见 §8-1），把已绑定的服从一个小区调到另一个小区（proxy 换集群同理）**必须解绑 + 重新人工确认**，不允许后台直接改派。流程采用「换区工单」编排（发起端点与流程归 `v2-zone-authority.md` §4.7 权威）：后台对已绑定服发起换区（原因必填 + 影响预览）→ 同一事务内身份走 T10 解绑（操作者 = 发起人）、server 解除原归属并记录预填目标 → agent 因绑定失效回退注册循环，经 T11 自动重入 `pending`（待确认列表带「换区中」提示与预填目标）→ 管理员经 T3 **重新人工确认**，同一事务按确认目标落区（默认取预填、确认人可改，含改为「暂不分配」）→ 恢复 active。**换区期间该服不可调度**：身份非 active 命中 P4 既有 `pending_confirm` 原因，原归属已解除另命中 `unassigned`，不新增原因码。本规格约束两点：① 换区必须经后台受控操作，agent 无任何自行声明/变更 zone 的通道（ADR-0004 延续）；② 发起、解绑、重确认、落区全部写审计（§4.7）。换 **serverId** 或换 **namespace** 同样走 T10 解绑 → T11 重新注册 → T3 重新人工确认的完整链路，不提供任何「原地改名」捷径。

**disabled 语义**（默认决定，见 §8-7）：禁用保留绑定与可观测性——agent 注册/身份校验仍通过（返回 `disabled` 状态让 agent 知晓），但身份域向 P4 输出「不可调度」，且控制面不向其下发任何指令。用于「临时摘除某台服但不想重走确认」的运维场景。

### 4.4 冲突象限（FR-141）

注册请求到达时，按 `identityId`、`(namespace, serverId)` 两个维度比对既有活跃绑定，处置矩阵如下：

| 象限 | 场景 | 典型成因 | 处置 |
|---|---|---|---|
| Q1 | 同 identityId + 同 namespace + 同 serverId，新 bootId / 新地址 | 正常重启；**故障换机**（数据目录随迁到新机、IP 变化） | 直接恢复：更新 `boot_id` / `last_addr`，返回当前状态（active→200）。地址变化时写一条审计（操作者 system，动作 `identity.address_changed`）。**不判冲突、不误杀**（§4.6） |
| Q2 | 同 identityId，不同 serverId（或不同 namespace） | 复制目录后改了 serverId；运维误改 serverId / 迁移 namespace 未解绑 | **拒绝注册**：409 `{code:"identity_binding_mismatch"}`，响应里带当前绑定的 serverId 供运维自查；不改动既有绑定；写审计（system）。正确路径 = 后台解绑（T10）后重注册 |
| Q3 | 不同 identityId，同 namespace + 同 serverId（该 serverId 已被 active/disabled 绑定占用） | 换新机没迁身份文件；两台服误配同 serverId | 新身份**允许进入 `pending`**，行上带占用冲突标记（`conflict_reason = "server-id-occupied"`，状态仍是 pending）；待确认列表醒目提示。管理员确认该 pending 时**必须显式勾选「解绑旧身份」**：同一事务内旧身份走 T10、新身份走 T3，双方各写审计。不勾选则确认被拒绝 |
| Q4 | 同 identityId + 同 serverId，**并发双实例**（bootId 交替） | 整目录复制未改任何配置，两份同时在跑 | 检测见 §4.5：身份转 `conflict`（T12），**双实例都不可调度**，告警 + 审计。处置见 T13/T14 |

补充：不同 identityId + 不同 serverId 是正常新接入（走 T1）；Q3 中若占用方是 `pending`（两台待确认抢同一 serverId），后到者注册直接 409 `{code:"server_id_pending_elsewhere"}`，避免待确认列表出现二义。

### 4.5 复制目录重复身份识别（FR-139/141）

**信号**：bootId。同一 identityId 的合法生命周期里，任一时刻只应有一个活跃 bootId；进程重启产生新 bootId 会**先经注册**刷新。因此：

- 服务端内存注册表记录每个 identityId 的当前 bootId。
- 收到带 `X-Beacon-Identity` 的请求时，比对请求携带的 `X-Beacon-Boot` 头与注册表：不一致且**未经过注册刷新** → 记一次「陈旧 bootId 请求」。
- **判定规则**：在 `identity-conflict-window`（运维设置，默认 10 分钟）内，同一 identityId 出现 **≥2 个不同 bootId 交替活跃**（即 bootId A 刷新注册后又收到 bootId B 的活跃请求，再次往复）→ 判定并发双实例，触发 T12 转 `conflict`。单向切换（A 停、B 起，不再见 A）是正常重启/换机，不触发。
- 进入 `conflict` 后：双实例的请求都收到 409 `{status:"conflict"}`；agent 收到后停止业务上报、进入注册轮询等待处置，本地按 fail-static 继续跑（不影响玩家）。
- **处置**（T13）：管理员在详情页看到冲突双方的 bootId、来源地址、最近活跃时间，指定保留一方；控制面以保留方 bootId 为准恢复 active，落败方后续请求持续 409 且响应文案明确提示「本实例身份已被判为副本，请删除本目录下 identity.yml 后按新身份重新接入，或直接下线本实例」。控制面不远程删除 agent 文件（agent 面对控制面只读暴露原则的对偶：控制面不伸手改 agent 本地身份）。

### 4.6 故障换机不误杀（FR-141 / Legacy 高风险区延续）

场景：某台服硬件故障，运维把整个数据目录（含 `identity.yml`）迁到新机器，新 IP 启动。

保证：

1. 新机注册命中 Q1：identityId、namespace、serverId 全一致，仅 bootId 与地址变化 → 直接恢复 active，**零人工干预**。
2. 旧机已死，不会再有旧 bootId 的活跃请求 → 不满足 §4.5「交替活跃」判定，不会误转 conflict。
3. 若旧机「假死复活」（迁移后旧机又被误启动）→ 恰好构成 Q4 并发双实例，转 conflict 冻结待人工处置——这正是期望行为，不是误杀。
4. 地址变更写审计，运维事后可追溯换机记录。

### 4.7 审计事件清单

以下事件全部写入通用审计（含操作者、namespace、identityId、serverId、原因、前后状态、traceId；系统动作操作者 = `system`）：

`identity.registered`（首次注册/重新申请）· `identity.approved` · `identity.rejected` · `identity.expired` · `identity.disabled` · `identity.enabled` · `identity.unbound` · `identity.conflict_detected` · `identity.conflict_resolved` · `identity.address_changed` · `identity.rebind_with_force_unbind`（Q3 换绑，一条审计同时记录旧身份解绑与新身份确认的关联）· `identity.reapply_allowed`。命名统一「点分小写 `<域>.<动作>`」形态（与 `cross_namespace.*`、`delivery.order.*`、`message.payload.view` 一致）。

换区工单的审计事件 `zone.rezone.initiated`（发起）与 `zone.rezone.completed`（重确认落区）归 zone 域权威定义（`v2-zone-authority.md` §4.7），事件里必须能关联到 identityId；换区链路中的解绑与重确认复用本域 `identity.unbound` / `identity.approved`，原因字段标明换区关联。

## 5. API 契约

错误响应统一 `{code, message, traceId}`（基座 §2）；时间 UTC ISO-8601。

### 5.1 agent 面（`/beacon/v2/agent/*`，鉴权：`X-Beacon-Token`；绑定后另带 `X-Beacon-Identity` 与 `X-Beacon-Boot`）

| 方法 | 路径 | 请求要点 | 响应要点 |
|---|---|---|---|
| POST | `/beacon/v2/agent/register` | body：`identityId`、`serverId`、`kind`(proxy/backend)、`bootId`、`agentVersion`；来源地址服务端自取 | 200 `{status:"active"\|"disabled"}` / 202 `{status:"pending", expiresAt}` / 403 `identity_rejected` / 409 `identity_binding_mismatch` \| `server_id_pending_elsewhere` \| `{status:"conflict"}` |
| GET | `/beacon/v2/agent/registration?wait=55` | 长轮询当前身份状态；`wait` 秒数（上限 60），状态无变化超时返回 304 | 200 `{status, serverId, namespace, reason?}`；304 无变化 |

确认前 agent 仅允许以上两个端点；其余 agent 面端点在身份中间件层对非 active/disabled 身份返回 403。

### 5.2 管理面（`/admin/v2/*`，鉴权沿用登录令牌 / API 密钥）

| 方法 | 路径 | 请求要点 | 响应要点 |
|---|---|---|---|
| GET | `/admin/v2/agent-identities` | query：`status`、`namespaceId`、`keyword`（匹配 serverId/identityId）、`page`、`pageSize`（服务端分页，面向 1000+） | `{items:[{identityId, namespaceId, serverId, kind, status, bootId, lastAddr, agentVersion, pendingExpiresAt, boundAt, statusChangedAt, conflictReason}], total}` |
| GET | `/admin/v2/agent-identities/{identityId}` | — | 单条详情；`conflict` 时附冲突双方 `{bootId, lastAddr, lastSeenAt}` 明细 |
| POST | `/admin/v2/agent-identities/{identityId}/approve` | body：`forceUnbindOccupier`(bool，Q3 占用冲突时必须为 true)；`target?:{kind:zone/bc_cluster, id}`（仅换区重确认时允许：缺省取预填目标，显式 `target:null` 表示确认但暂不分配） | 200；非 pending → 409 `illegal_state`；Q3 未勾选强制解绑 → 409 `server_id_occupied`；非换区中传 `target` → 400 |
| POST | `/admin/v2/agent-identities/{identityId}/reject` | body：`reason`（必填） | 200；非 pending → 409 |
| POST | `/admin/v2/agent-identities/{identityId}/allow-reapply` | body：`reason`（必填） | 200；非 rejected → 409 |
| POST | `/admin/v2/agent-identities/{identityId}/disable` | body：`reason`（必填） | 200；非 active → 409 |
| POST | `/admin/v2/agent-identities/{identityId}/enable` | — | 200；非 disabled → 409 |
| POST | `/admin/v2/agent-identities/{identityId}/unbind` | body：`reason`（必填） | 200；非 active/disabled/conflict → 409 |
| POST | `/admin/v2/agent-identities/{identityId}/resolve-conflict` | body：`keepBootId`（保留哪个实例）、`reason`（必填） | 200；非 conflict → 409；`keepBootId` 不在冲突双方内 → 400 |
| POST | `/admin/v2/agent-identities/batch-approve` | body：`identityIds[]`（上限 200/批）；换区中条目按各自预填目标落区（批量不支持改目标） | 207 风格逐条结果 `{results:[{identityId, ok, code?}]}`；含 Q3 冲突的条目单条失败不影响其余 |
| POST | `/admin/v2/agent-identities/batch-reject` | body：`identityIds[]`、`reason`（必填，整批共用） | 同上逐条结果 |

说明：

- approve 默认不内联 zone 分配（见 §8-8）；首次接入的分配走 zone 域批量分配端点，`/servers` 页面把两步串成向导。**例外**：换区重确认时 approve 同事务按确认目标落区（§4.3「换区」），避免运维填两遍表单。
- 所有写端点在 DB 事务内完成状态迁移 + serverId 占用校验 + `server` 行创建/解绑联动；事务提交后才对 agent 长轮询可见。
- 运维设置项（走系统设置域热更）：`registration-pending-ttl`（默认 72h）、`identity-conflict-window`（默认 10min）。

## 6. 与其他规格的边界

| 对方 | 我依赖它 | 我交付给它 |
|---|---|---|
| `v2-namespace-isolation.md` | token↔namespace 校验中间件；namespace 实体 | 身份绑定含 namespace 维度，跨 ns 重绑必须先解绑（其隔离审查的落点之一） |
| `v2-zone-authority.md` | `server` 实体权威结构；换区工单/批量分配端点与审计 | approve 时创建/关联 `server` 行；「未分配」= 有 active 身份但归属字段为空（backend 的 `zone_id` / proxy 的 `bc_cluster_id`）的判定输入 |
| `v2-metrics-health-scheduling.md`（P4） | 指标上报通道（兼活性信号，活性判定归其权威；身份中间件挂在其请求路径上做 bootId 比对） | 身份状态作为 schedulable 判定输入（非 active 一票否决）；内存注册表的身份摘除时机（T10/T12） |
| 通用审计机制 | 审计写入能力 | §4.7 事件清单 |
| `docs/API.md` | — | §5 端点表供其汇总 |
| 前端 `/servers`、`/namespaces`（P2 mock → P3 接真深化） | 页面契约按 `docs/UX.md` | 待确认列表、冲突处置、解绑/禁用操作的数据形状即 §5.2 |

## 7. 验收标准

对齐 PRD FR-139/140/141 验收摘要，逐条可验证：

**FR-139 身份文件**

1. 全新 agent 首启后数据目录出现 `identity.yml`，含合法 UUIDv4；重启 N 次 identityId 不变。
2. 删除身份文件再启动 → 生成新 identityId（等价新身份，需重走确认）。
3. 身份文件损坏（非法 YAML / 非法 UUID）→ agent 不注册、ERROR 日志给出处理指引、玩家进服不受影响（fail-static）。
4. 复制整个服目录起第二份实例 → 在 `identity-conflict-window` 内被判定 Q4 冲突，双实例不可调度，后台可见冲突明细并告警。

**FR-140 注册绑定**

5. 新 agent 注册后出现在待确认列表（`status=pending`）；确认前 agent 除 register/registration 外所有端点 403；schedulable 恒为否。
6. 管理员 approve → agent 长轮询秒级感知转 active；`server` 行已创建且归属字段为空（backend 的 `zone_id` / proxy 的 `bc_cluster_id` 为 NULL，未分配）。
7. reject（必填原因）→ agent 收到 rejected 后不再自动重试；后台 allow-reapply 后 agent 重注册可回 pending。
8. pending 超过 `registration-pending-ttl` 未处理 → 自动转 expired（审计操作者 system）；agent 重注册回 pending。
9. 确认、拒绝、过期、重新申请各产生一条审计，含操作者、原因、前后状态、traceId。

**FR-141 冲突 / 解绑 / 换区 / 禁用 / 重新绑定**

10. Q2（同 identityId 改 serverId）注册被 409 拒绝，既有绑定不受影响；解绑后重注册成功回 pending。
11. Q3（新身份抢占已绑定 serverId）进入 pending 且带占用标记；不勾选强制解绑的 approve 被 409 拒绝；勾选后同一事务完成旧解绑 + 新确认，两侧审计可关联。
12. 故障换机（同 identityId + serverId，新 IP 新 bootId）注册即恢复 active，无人工干预、不进 conflict；地址变更有审计。
13. disable 后该服不可调度、不接收指令，但注册与后台可观测正常；enable 恢复。
14. unbind 后旧绑定失效：旧 agent 后续请求 403/409，不能继续以旧绑定误用；同 identityId 重注册开启新 pending，可换 serverId 或 namespace。
15. conflict 处置指定保留实例后，保留方恢复 active，落败方持续收到 409 及明确的人工处理指引文案。
16. 状态迁移表（§4.3）之外的任何迁移调用返回 409 `illegal_state`，状态机穷举单测覆盖全部合法/非法迁移。
17. 换区走完整解绑重确认闭环：发起换区后身份转 unbound、服归属清空且不可调度（`pending_confirm` + `unassigned`）；agent 自动重入 pending；重新人工确认（目标预填可改）后落区恢复 active；发起 / 解绑 / 重确认 / 落区审计齐全且可关联 identityId；不存在绕过解绑重确认的直接改派通道；agent 无任何自行变更 zone/serverId 的通道。
18. 全链路数据库操作不使用 MySQL 专有特性（枚举 VARCHAR、无部分索引依赖），SQLite/Postgres 兼容路径可跑通建表。

## 8. 风险 / 待定

以下为撰写时替用户做的**默认决定**，需拍板确认；正文均已按默认值写死，拍板变更后同步修订。

1. **「换区必须后台解绑」的解读——已拍板**：PRD §3 原文「换区或换 serverId 必须后台解绑」。**2026-07-07 拍板为字面解读：换区必须解绑 + 重新人工确认（宽松解读「换区 = 不打断 active 绑定的受控改派」被否）**。正文已按字面语义修订（§4.3「换区」+ `v2-zone-authority.md` §4.7 换区工单）；为避免运维填两遍表单，采用工单预填目标 + 重确认落区的编排。
2. **pending 过期时长**：默认 72h，走运维设置热更（`registration-pending-ttl`）。
3. **身份文件损坏处理**：不自动重生成、不注册、ERROR 日志待人工——牺牲自动恢复换取「绝不静默漂移身份」。
4. **rejected 不允许 agent 自动重新申请**：必须管理员 allow-reapply（转 expired）后才能重注册；拒绝语义从严。
5. **`agent_identity` 单行模型**：一个 identityId 一行，重新绑定复用行、历史靠审计流水，不建绑定历史表（简单优先）。若后续需要「绑定历史」页面再立 FR。
6. **并发双实例判定参数**：窗口默认 10 分钟（`identity-conflict-window`），窗口内 ≥2 个不同 bootId 交替活跃即判冲突；阈值过敏/过钝需真机调参。
7. **disabled 语义**：保留注册与可观测、仅摘除调度与指令下发；不是「彻底拉黑」（拉黑用 reject/unbind）。
8. **approve 不内联 zone 分配**：确认与分配分两步（前端向导串联），保持身份域与 zone 域边界干净；代价是最少两次操作。唯一例外是换区重确认（按工单预填目标同事务落区，见 §4.3「换区」与 §8-1 拍板）。
9. **控制面不远程清除 agent 身份文件**：冲突落败方只收 409 与指引文案，物理清理靠运维——避免控制面对 agent 本地文件有写权力。
