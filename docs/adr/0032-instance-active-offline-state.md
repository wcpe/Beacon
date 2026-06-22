# ADR-0032：实例主动下线态——落 DB 拒绝接入，区别于 drain 与健康 TTL

**状态**：已接受

## 背景

管理台的「下线」操作（`POST /instances/{id}/offline`）此前只调 `Registry.Offline` 从内存注册表删条目，不落任何持久态。后果：

1. **删了等于没删**：被删实例的 agent 下一跳心跳收到 `NOT_REGISTERED` → 立即重新注册复活，运维"下线"无实际效果，且伴随重连日志刷屏。
2. **控制面重启即遗忘**：内存态无持久化，重启后所有"下线"痕迹消失。

运维真实诉求是「把某台子服按死、不让它再接入，直到我显式放行」——这是一个**主动运维决策态**，而非健康衰退。

这里要先厘清三个**容易混淆**的状态机，否则极易把它们揉成一个污染彼此语义：

- **健康 TTL（FR-5/FR-28）**：心跳陈旧度自动衰退 `online→degraded→lost→offline`，真源是内存注册表，**自动、可自愈**，不阻断接入。注意这里的 health-`offline` 是"心跳老到没影了"的**自动观测态**，与本 ADR 的"主动下线"**同名但语义不同**。
- **drain（FR-10，[ADR-0017](0017-traffic-scheduling-decision-vs-execution.md)）**：排空 / 维护标记，落 DB `server_drain`，语义是"别再往这台送新玩家，但它**仍可连接**、已在的玩家不受影响"，只影响落位候选。
- **主动下线（本 ADR）**：运维按死该实例，**拒绝其注册接入**，落 DB 持久，须显式取消。

[ADR-0017](0017-traffic-scheduling-decision-vs-execution.md) 当初明确把 drain 之外的 offline/online 划在范围外（只做"排空"不做"按死"）。FR-49 是对该范围边界的**显式升级**：在不改 drain 语义的前提下，新增一条独立的"主动下线"状态机。

## 决策

1. **新增独立的下线拒绝表 `server_offline`，与 `server_drain` 同构但职责分离。** 表结构照搬 `ServerDrain` 范式（`namespace_code` / `server_id` / `reason` / 软删 `deleted_at` 哨兵 + 唯一键），GORM AutoMigrate、零方言绑定（VARCHAR、不用 `ENUM/JSON`、软删用哨兵值而非 NULL），可移植。**它与 `server_drain` 是两张表、两套语义，绝不复用同一张表加 type 字段区分**——避免把"排空"和"按死"耦进一个状态机。记录存在即"已下线"，取消即软删（沿用既有软删模式）。

2. **下线在 register 处拦截，不在 heartbeat 热路径查库。** `InstanceService.Register` 在写内存注册表前先查 `server_offline`，命中 → 返回专门错误码 `INSTANCE_OFFLINE_REJECTED`（HTTP 403）。**心跳（heartbeat）不查库**：主动下线某在线实例时同步把它移出内存注册表，其下一跳心跳自然收到 `NOT_REGISTERED`(404) → agent 触发重新注册 → 在 register 处被拒。如此下线态收敛只需一次 register 查库，心跳保持 DB-free（不引入每跳 DB IO / N+1）。

3. **`POST /instances/{id}/offline` 语义收敛为「落拒绝态 + 移出可用集」，新增 `DELETE …/offline` 取消下线（uncordon）。** 选择**改既有端点语义**而非新开端点：原端点本就叫 offline、前端按钮也叫"下线"，把它从"内存删除"升级为"持久拒绝 + 移出内存"是同一意图的增强，不另造概念。下线写 `server_offline` + 审计 `instance.offline`、提交后移出内存 + 唤醒拓扑 watch（事务提交成功才触发 watch）；取消下线软删 `server_offline` + 审计 `instance.online`，**不主动复活实例**（等 agent 降频探测重连或运维 `reconnect`）。下线对不在内存的实例也允许（可预先按死离线 / 未注册的 serverId）。

4. **专门错误码区分三类拒绝。** `INSTANCE_OFFLINE_REJECTED`(403) ≠ `NOT_REGISTERED`(404，自然失联 / 未注册) ≠ `DUPLICATE_SERVER_ID`(409，故障换机守卫)。agent 据此进入与"控制面不可用"截然不同的处置分支。重复 serverId 守卫逻辑保持不变（先查下线、再走注册表守卫，互不干扰）。

5. **agent 收 403 进新生命周期态 `OFFLINE`，停止猛重连、降噪日志，fail-static 不变。** `BeaconApiClient.register` 把 403 映射为 `RegisterOutcome.OfflineRejected`；`AgentLifecycle` 收到后进 `AgentState.OFFLINE`，**不走退避猛打**，改按一个远大于退避上限的大间隔安排降频探测重注册（取消下线后探测成功即恢复 RUNNING）；日志只在首次进入 OFFLINE 时 WARN 一次，后续降频探测失败按 DEBUG。**`OFFLINE`（被主动下线）与 `DEGRADED`（控制面不可用）严格区分**：被拒不清快照、不阻断玩家进服（玩家照常按本地有效配置运行），fail-static 红线不破。core 保持 TabooLib-free、不在主线程阻塞 IO（探测仍走 async 适配器）。

## 理由

- **三态分表分语义**，让"健康自愈 / 排空不送人 / 按死不让连"各自清晰、可单测、可审计，杜绝把运维决策塞进健康状态机造成的语义污染。
- **register 拦截 + 心跳移内存**的收敛链路最省：下线态权威在 DB（持久、可重启存活），但只在低频的 register 路径查库，心跳保持热路径无 DB IO，不破坏既有健康 TTL 的内存真源边界（注册 / 健康仍以进程内存为真源，下线拒绝是 register 的一道前置闸）。
- **复用 drain 的软删 + 事务 + 审计模式**，代价低、可移植，与 `zone_assignment` / `server_drain` / `api_key` 同源类别。
- **专门错误码 + 专门 agent 态**，让 agent 能"被按死时安静待命、控制面挂时按快照硬扛"——两种完全不同的失败语义不再被笼统当作"连不上"猛重连刷屏。

## 后果

- 多一张 `server_offline` 表与一个 `ServerOfflineRepository`；`InstanceService` 从纯内存编排变为持有 `db` + `offlineRepo`（注册路径多一次按 ns+serverId 的点查，非 N+1）。
- 下线收敛依赖「移出内存 → 心跳 404 → 重注册被拒」链路。若未来心跳改为可独立复活实例（不经 register），需重新评估是否在心跳侧也加查库或改走内存下线标记。
- 取消下线后实例恢复有延迟（等 agent 降频探测周期），运维可经 `reconnect` 运维命令即时拉起，可接受。
- health 状态字典里的 `offline` 与本 ADR 的"主动下线"同名不同义；前端 / 文档须明确区分（健康 `offline`=失联到极限，主动下线=被运维按死、记录在 `server_offline`）。

## 备选方案

- **下线只删内存、不落 DB（保持现状）**：删了即被 agent 重连复活、重启遗忘，运维不可用。被否（本 ADR 即修此缺陷）。
- **复用 `server_drain` 加 `type` 字段区分 drain / offline**：把两套语义耦进一张表 / 一个状态机，违反"三态职责分离"，且 drain 不阻断接入、offline 阻断接入，落位 / 注册两条读路径都要带条件判断，复杂且易错。被否。
- **心跳也查库拒绝下线实例**：每跳心跳一次 DB IO，高频 N+1，违反性能约束；且与"心跳热路径不碰 DB"边界冲突。被否（改用 register 拦截 + 移内存收敛）。
- **agent 收 403 后彻底停止一切循环、不再探测**：取消下线后无法自动恢复，必须人工 reconnect 每一台，运维负担重。被否（改用大间隔降频探测，兼顾"不刷屏"与"可自动恢复"）。
- **新开 `/instances/{id}/cordon` 等独立端点**：另造与现有"下线"按钮平行的新概念，增加认知与 UI 负担；现有 offline 端点语义升级即可覆盖。被否。
