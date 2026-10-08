# 功能规格：交付编排可靠性（提审事务化 · 终止目标收口 · 推进器停滞自愈 · 观察窗内存与预热口径）

> 状态：开发中　·　关联 PRD：FR-259、FR-260、FR-262、FR-265　·　分支：feature/fr-259-orchestration
> 承接：[v2-delivery-orchestration](v2-delivery-orchestration.md)（状态机 §4.1 / 熔断 §4.4.4 / 推进门 §4.4.5 / 观察窗 §4.6.3 / 回滚 §4.7）、[delivery-rollback-resilience](delivery-rollback-resilience.md)（回滚模型，本组不动）
> 决策：[ADR-0079](../adr/0079-principal-capability-and-dangerous-operation-approval.md)（危险操作统一审批）、[ADR-0057](../adr/0057-surface-desensitized-errors.md)（错误脱敏）

## 1. 背景与目标

[v2-delivery-orchestration](v2-delivery-orchestration.md) 定下的状态机在 happy path 上成立，但**四条「走岔即不可恢复」的路径**在两轮真机验收里被逐一撞出来：

1. **提审两步非事务，第二步失败即冻死单据（FR-259）**：`RequestSubmit` 先 `applySubmit`（draft→`pending_approval`）再 `requestApprovePending`（建审批申请），两步各自开事务。第二步失败（前端漏带 `Idempotency-Key` → `400 INVALID_PARAM`）时第一步已提交，单被冻结在 `pending_approval` 却**没有对应的审批申请**：推不动（没有申请可批准）也退不回（`submit` / `cancel` 均 `illegal_state`），只能人工改库。批 1 验收在单 1 / 单 4 上实测复现。
2. **审批票据执行失败后单据不可恢复（FR-259 形态二）**：审批票据**执行**失败（真机 `start_conflict` 目标集冲突）时票据已 `failed`（终态、不可撤回），而 `RequestSubmit` 见单是 `pending_approval` 一律回 `409 illegal_state`。单据卡在「有状态无申请」，重提不成、成交不成。——波次 A 验收 O1 在单 #14 / #19 上实测复现。
3. **终止变更单留下在途孤儿目标（FR-260）**：`Cancel` 只把 `pending` 目标置 `skipped`，不处置 `pushing` / `pushed` / `activating`。而 `cancelled` **不在**推进器装载集 `deliveryActiveOrderStatuses` 内——终止后推进器永不再看这张单，这些在途目标既不通向 `activated` 也不通向 `failed`，成为「既不推进也不收尸」的孤儿，状态墙长期停在中间态。
4. **编排推进器停滞完全静默（FR-262）**：推进器每 2s 空转一轮，两类「已无事可做且不会自行恢复」的停滞此前**没有任何日志 / 告警 / 事件**：批已到 `awaiting_confirm` 等人工确认、以及 rolling 单**根本没有活动批**（批被并发迁走或数据不一致）。运维只看到「单还 rolling 但不动了」，只能翻库猜原因。同时 `resumeCircuitBatch` / `tripBreaker` 丢弃 `UpdateBatchCAS` 的命中结果，并发迁移落到「批未 failed 而单已 paused」的半截状态也无人知晓。
5. **观察窗内存只进不出 + 冷启动口径冲突（FR-265）**：自动收单（`advanceRollingBack` 的 `autoFinishRollback`）是唯一不经审批执行适配器的终态出口，而 `clearObserve` 只在确认批与人工终止两条路径上被调用——控制面长跑时终态单的观察窗缓冲与停滞观测无界累积。另 `restartHealthWarmup`（90s）大于过短的观察窗时，restart 目标在**整段**观察窗里都被排除出健康恶化评估，健康恶化熔断恒不触发（分母恒为 0），而界面看不出「配了但永不生效」。

**目标**：把四类「不可恢复 / 不可观测」的形态各自收口——要么两步同生共死，要么至少让它可见、可重试、可自愈；组单期的口径冲突则在组单那一刻就前置拒绝。

**范围内**：提审事务边界、`pending_approval` 死结自愈、紧急终止的目标 / 批收口、推进器停滞检测与告警、CAS 命中口径统一、观察窗内存释放与观察窗 / 预热组合防呆。
**范围外**：幂等语义收口（FR-263）、回滚模型与回滚记录（FR-262r / FR-270 / FR-271）、agent 侧生效与备份（FR-266~269）、blob 治理（B2 组）。

## 2. 需求（要什么）

### 2.1 FR-259：交付提交事务化

- 提审的「冻结单据」与「创建审批申请」必须**同生共死**：任一失败则单停在可编辑的 `draft`，运维修正后可原样重试。
- 不得产生「有状态无申请」的中间态——这是所有死锁的共同根。
- 「有状态无申请」**已经发生**时（历史数据 / 票据执行失败）必须可自愈：`pending_approval` 且无未终结申请的允许重提补建申请。
- 自愈不得被滥用：仍有未终结申请（`pending` / `executing`）时重提必须拒绝，避免叠出两条并行的同单审批。
- 幂等键缺失等入参错误必须在冻结**之前**或在回滚之后可见，绝不留副作用。

### 2.2 FR-260：变更单终止的目标收口

- `cancel` 必须把单内 `pushing` / `pushed` / `activating` 目标一并收口到终态。
- 收口语义须与目标的实际盘面事实自洽：己动过盘的收 `failed`（可被回滚覆盖），从未动盘的 `pending` 仍按既有口径收 `skipped`。
- 承载在途目标的活动批（`running` / `observing` / `awaiting_confirm`）同样要收终态并补 `finished_at`，不得停在非终态。
- 收口须留下可读原因，且收口是一次性终态：终止后推进器不得再改这些目标的状态。

### 2.3 FR-262：编排推进器停滞检测与自愈

- 两类停滞必须**可见**（WARN 日志含单号与停滞时长）而非静默空转。
- 提醒须按节律去重：不到点不报、过了首报点后按间隔重复，不得每 2s 刷屏。
- 停滞解除（人工确认 / 单终态化）后停止提醒并释放观测状态。
- 正常推进中不得误报。
- CAS 口径统一：批 CAS 命中与否必须显式判，未命中要回报冲突（或回滚事务）而非静默继续提交。

### 2.4 FR-265：观察窗内存释放 + 预热口径防呆

- 自动收单路径必须释放观察窗内存缓冲与停滞观测。
- restart + 健康恶化阈值开启 + 观察窗 < `restartHealthWarmup`（90s）的组合必须在组单 / 编辑期显式拒绝并给出原因；其它组合（非 restart、健康恶化阈值关闭、观察窗 ≥ 90s）不受影响。

## 3. 设计（怎么做）

### 3.1 FR-259：同事务 + 幂等自愈

`RequestSubmit` 改为 `requestSubmitInTx`（[delivery_order_service.go](../../apps/server/internal/service/delivery_order_service.go)）：外层开一个事务，事务内依次执行

1. `prepareSubmit`：`draft → pending_approval`（前置校验 + CAS + 提审计）；
2. `requestApprovePending`：创建审批申请。

任一步返回错误即整体回滚，单停在 `draft`。

**审批服务必须绑到同一个 tx**（`withTx` 同包副本写法，与既有 `executeDeliveryApprovalInTx` 同款）：`ApprovalService.request` 自身会开事务，在外层事务里嵌套调用会让单连接库（sqlite）自锁，MySQL 上则退化为两个独立事务——两步又变回非事务。

自愈判定放在 `prepareSubmit` 开头（死结形态二）：

- 单已是 `pending_approval` → `reconcilePendingApproval` 查 `delivery.approve` 下该单**未终结**申请数（`pending` / `executing`）；
- 有 → `409 illegal_state`（防叠申请）；
- 无 → 放行，本轮只补建申请（单状态本来就对）。

`countLiveDeliveryApprovals` 的「未终结」口径刻意只取 `pending` / `executing`：这两态还可能产生副作用；`rejected` / `withdrawn` / `expired` / `failed` 已不再推进，不构成重提冲突。

### 3.2 FR-260：终止时的目标与批收口

`Cancel` 在原有 CAS 之后、目标 `skipped` 之前插入 `settleInFlightTargets`（[delivery_orchestrator_control.go](../../apps/server/internal/service/delivery_orchestrator_control.go)），
对 `cancelInFlightStatuses` = `pushing` / `pushed` / `activating` 逐个批量 CAS 到 `failed`，并落统一脱敏原因 `inFlightTargetCancelReason`。

**为什么取 `failed` 而非 `skipped`**：`skipped` 的语义是「从未开始、未动盘」，而这三态都**已经动了盘**（推送覆盖或已下发生效命令）。标 `skipped` 会让运维误判「这台没被碰过」，从而错过回滚；且这些目标 `pushed_at` 已非空、属回滚候选，只有 `failed` 与「曾覆盖磁盘」的事实自洽。

活动批（非 `pending`）一并收 `skipped` + `finished_at`：批停在非终态会使其 `finished_at` 恒空、终态批事件永不派生，也与同单已收口的批形态不一致。

收口计数进审计 `detail.settled`，使「终止时扫了多少在途目标」可追溯。

### 3.3 FR-262：停滞检测

新增 `stallByOrder`（按单索引的 `deliveryStallState`：`kind` / `since` / `remindedAt`），在 `advanceOrder` 每轮末尾调 `detectStall`：

| 停滞类型 | 触发条件 | 首报点 | 重复间隔 |
| --- | --- | --- | --- |
| `confirm_gate` | 活动批 `awaiting_confirm` | 5 min | 30 min |
| `no_active_batch` | rolling 且 payload ready 但无活动批 | 2 min | 10 min |

正常推进/rolling且payload未就绪/ rol paused / `rolling_back` 一律判为「未停滞」并 `clearStall`（先前 carried 的计时清零，下次重算）。

节律常量集中在 [delivery_orchestrator.go](../../apps/server/internal/service/delivery_orchestrator.go)，均为控制面内部节律、非运维旋钮（与 `deliveryTickInterval` 同口径）。

**CAS 口径统一**：`tripBreaker` 与 `resumeCircuitBatch` 的 `UpdateBatchCAS` 改为显式判命中，未命中分别走 `errCASSkip`（回滚事务）与 `changeIllegalState`（回报冲突），不再丢弃命中值继续提交——避免「批未 failed 而单已 paused / rolling」的半截状态。

### 3.4 FR-265：内存释放与预热防呆

- `clearObserve` 顺带 `clearStall(orderID)`：单已收口即不再会停滞，留着只是内存垃圾；
- `autoFinishRollback` 在置终态成功之后调 `s.clearObserve(rt.order.ID)`——这是自动收单唯一缺失的释放点；
- `validateObserveWindowCombination` 在 `applyOrderInput` 末尾（创建与编辑共用）拒绝 `restart` + `unhealthyRateThresholdPercent > 0` + `observeWindowSec < 90` 的组合，错误文案点明「健康恶化熔断永不触发」。

## 4. UX / 交互

纯后端 FR，无页面改动。面向调用方的可感知变更集中在 API 的错误与拒绝文案上：

- FR-259 形态一（缺幂等键）不再留下副作用：单停在 `draft`，调用方补上幂等键后重新提审即可，与旧行为（冻结在 `pending_approval` 且不可恢复）形成对比；
- FR-265 组单拒绝文案直陈后果（见 §3.4），使运维当场知道该把观察窗调大还是改用非 restart 生效方式。

## 5. 任务拆分

- [x] FR-259：提审两步收敛到同一事务（含审批服务 tx 绑定）+ 死结自愈 + 红绿转换
- [x] FR-260：`cancel` 收口在途目标与活动批 + 收口审计 + 红绿转换
- [x] FR-262：停滞检测与节律告警 + CAS 命中口径统一 + 红绿转换
- [x] FR-265：自动收单释内存 + 观察窗 / 预热组合防呆 + 红绿转换
- [x] 文档同步：PRD 新增 FR-259 / FR-260 / FR-265 行、FR-262 行补 IEA 覆盖、API.md、CHANGELOG 未发布段

## 6. 验收标准

- **FR-259**
  1. 缺 `Idempotency-Key` 提审 → `400 INVALID_PARAM`，单**仍为 `draft`**（非 `pending_approval`），库内 0 条审批申请、0 条提审计；
  2. 同一单补上幂等键重试 → 成功返回票据且单 `pending_approval`；
  3. `pending_approval` 单 + 关联申请已 `failed`（形态二）→ 重提被接受并补建申请，单仍在 `pending_approval`，未终结申请恰 1 条；
  4. `pending_approval` 单 + 未终结申请仍存在 → 重提被拒（`409 illegal_state`）且不建第二条申请。
- **FR-260**
  5. 单内含 `pushing` / `pushed` / `activating` 各一台时终止 → 三台全部 `failed` 且带终止原因，库内不再残留在途态目标；
  6. 批含活动批时终止 → 全部批 `skipped` 且 `finished_at` 非空；
  7. 终止后再跑两轮推进器，目标状态不被改回 / 不再变化；
  8. 从未开始的 `pending` 目标仍置 `skipped`（不是 `failed`），既有语义不回退；
  9. 空原因终止仍被拒。
- **FR-262**
  10. 批进 `awaiting_confirm` 后：未到 5 min 不告警 → 过 5 min 告警一次且含「推进门」「等待人工确认」 → 未到 30 min 不重复 → 过 30 min 再提醒 → 人工确认完成（单 `completed`）后不再提醒且停滞观测被清；
  11. rolling 单无活动批：未到 2 min 不告警 → 过 2 min 告警且含「没有活动批」；
  12. 正常推进（批 running 或有目标在途）不告警且不留停滞观测；
  13. 批 CAS 未命中时 `tripBreaker` 回滚事务、`resumeCircuitBatch` 报冲突，均不再落到半截状态。
- **FR-265**
  14. 自动收单（`rolling_back` → `rolled_back` 全自动出口）后释放观察窗缓冲与停滞观测；
  15. `restart` + 健康恶化阈值 > 0 + 观察窗 60s → 编辑与创建均被拒；观察窗 90s（等于预热宽限）→ 放行；关闭健康恶化阈值或非 restart 生效方式 → 短窗放行。

## 7. 风险 / 待定

- **自愈的幂等边界**：形态二放行重提依赖「审批申请已终结」这一判据。若某条申请的 worker 正在 `executing`（lease 未释放），它确实仍会产出副作用，此时重提被拒是正确的；但 lease 过期而未终绪的申请会被判为未终结从而长期挡住重提（worker 会重试并最终置终态，故最终自愈）。待定：是否需要给「长时间 `executing` 且 lease 过期」加一条强制回收。
- **终止收口取 `failed` 对界面的影响**：状态墙上这些目标从「在途」变「失败」，止损后一眼看到的失败数会上升。这是刻意取舍——真盘面已动过，`skipped` 是假象。若将来界面要区分「终止导致」与「执行失败」，应展 / expand `error` 文案（已含「已紧急终止」），而不是改状态值。
- **停滞只报日志、不进告警事件流**：本组只用 `slog.Warn`，不接入告警中心（否则「正常等人工确认」会在告警中心刷屏）。若将来运维要求「卡住就告警」，应先确定阈值口径与降噪策略（同一单去重、相邻时段合并），再单独立项。
- **观察窗 / 预热组合的边界**：90s 是 `restartHealthWarmup` 的当前取值与默认观察窗 120s 之间的差；若将来调整任一值，本防呆的条件需同步审查（放置在一个函数内便于改动）。
- **真机维度**：本组尚未跑真机演练，上述验收仅在单测层验证，真机结论见汇报。
