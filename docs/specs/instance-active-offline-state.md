# 功能规格：实例主动下线态

> 状态：开发中　·　关联 PRD：FR-49　·　分支：feature/fr-49-instance-offline-state

## 1. 背景与目标

当前管理台的「下线」（`POST /instances/{id}/offline`）只是从内存注册表删条目（`Registry.Offline`），
不落任何持久态。在线 agent 下一跳心跳收到 `NOT_REGISTERED` 后立刻重新注册复活，删了等于没删，
并伴随重连日志刷屏。运维想要的是**真正把某台子服按下不动**：标记下线后该实例不再被允许接入，
直到运维显式取消。

本 FR 把「下线」做成**完整的主动下线态**：落 DB 拒绝状态（持久、控制面重启仍生效），
agent 注册被专门错误码拒绝后进入「下线态」停止重连、不刷日志，取消下线后可重新接入。
属 P2，显式扩展 ADR-0017 当初「只做 drain、不做 offline/online」的范围边界。

## 2. 需求（要什么）

- beacon 主动下线某实例 → 落 DB 拒绝状态；控制面重启后仍拒绝该实例接入。
- agent 注册被拒（专门错误码，区别于自然 lost/offline、区别于 `NOT_REGISTERED`）→ 进入「下线态」，
  停止重连（大幅降频、不退避猛打）、不刷错误日志。
- 取消下线（清除拒绝状态）后，agent 可重新接入（运维侧 reconnect 或下次降频探测）。
- 前端「下线」操作不再强制先筛环境——按行直接下线，namespace 从该行取。
- 与健康 TTL 的 online/lost/offline 语义可区分（可观测、不混淆）。

### 范围内
- 新增下线拒绝表 `server_offline`（软删范式，与 `server_drain` 同源类别）。
- `POST /instances/{id}/offline` 语义收敛为「落拒绝态 + 移出内存可用集」；新增 `DELETE /instances/{id}/offline`（取消下线）。
- 注册（register）前查拒绝表，命中返回 `INSTANCE_OFFLINE_REJECTED`（HTTP 403）。
- agent 新增 `OFFLINE` 生命周期态 + 降频探测重连。
- 前端按行 namespace 直接下线 + 取消下线。

### 不做（范围外）
- 不在心跳（heartbeat）热路径查库（避免每跳 DB IO / N+1）；下线靠「移出注册表→心跳 404→重注册被拒」收敛。
- 不做下线原因的复杂工作流 / 审批；reason 为可选自由文本（与 drain 一致）。
- 不动 drain（排空、仍可连）与健康 TTL（自动衰退）的既有职责。
- 不引入定时器主动驱逐已下线实例（移出内存即足够，健康扫描不复活已删条目）。

## 3. 设计（怎么做）

三者职责严格区分，互不混用：

| 机制 | 真源 | 语义 | 是否阻断接入 |
|---|---|---|---|
| 健康 TTL（FR-5/FR-28） | 内存注册表 | 心跳陈旧度自动衰退 online→degraded→lost→offline | 否（自动、可自愈） |
| drain（FR-10） | DB `server_drain` | 排空：不再往该服送新玩家，**仍允许连接** | 否（仅落位剔除） |
| 主动下线（FR-49） | DB `server_offline` | 运维按死该服：**拒绝其注册接入** | 是（register 403） |

### 控制面
- **存储**：新增 `server_offline` 表（`internal/model/server_offline.go`），完全照搬 `ServerDrain` 的同构范式
  （`namespace_code`/`server_id`/`reason`/软删 `deleted_at` 哨兵 + 唯一键），GORM 可移植（VARCHAR、不用方言专有），
  `store/db.go` 的 AutoMigrate 注册。
- **仓库**：`ServerOfflineRepository`（`FindByServer`/`Upsert`/`SoftDelete`/`ListActive`），照搬 drain 仓库。
- **服务**：`InstanceService` 注入 `db` + `offlineRepo`：
  - `Register` 在写注册表前查 `offlineRepo.FindByServer`，命中 → 审计 `instance.register`(fail) + 返回 `ErrInstanceOfflineRejected`。
    重复 serverId 守卫逻辑保持不变（先查下线、再走注册表守卫）。
  - `Offline(ns, serverID, reason, operator, clientIP)` 改为：事务内 `offlineRepo.Upsert` + 审计 `instance.offline`，
    提交成功后再 `registry.Offline` 移出内存 + 唤醒拓扑 watch。**不存在内存条目也允许下线**（可对离线/未注册实例预先按死）。
  - 新增 `Online(ns, serverID, operator, clientIP)`：事务内 `offlineRepo.SoftDelete` + 审计 `instance.online`；
    不存在拒绝态返回 `ErrOfflineNotFound`。清除后不主动复活（等 agent 降频探测重连）。
  - `ListOffline(ns)`：供前端 / 状态区分展示。
- **错误码**：新增 `ErrInstanceOfflineRejected`（403 `INSTANCE_OFFLINE_REJECTED`）与 `ErrOfflineNotFound`（404 `OFFLINE_NOT_FOUND`）。
- **action 枚举**：新增 `ActionInstanceOnline = "instance.online"`（`instance.offline` 已存在）。
- **handler/路由**：`POST /instances/{serverId}/offline` 改为带可选 `reason` body；新增 `DELETE /instances/{serverId}/offline`（取消下线）。
  二者均为写方法，经既有 `readonlyWriteGuard`，readonly 密钥 403。

### agent（core，TabooLib-free）
- `BeaconApiClient.register` 把 HTTP 403 映射为新 `RegisterOutcome.OfflineRejected`。
- `AgentState` 新增 `OFFLINE`：被控制面主动下线，停止常规重连循环、降噪。
- `AgentLifecycle.doRegister` 收到 `OfflineRejected` → 进 `OFFLINE` 态，**不退避猛打**，
  按一个**大间隔**（`offlineProbeIntervalMs`，默认远大于退避上限）安排一次降频探测重注册；
  探测成功（取消下线后）→ 正常 RUNNING；仍被拒 → 继续降频探测。日志只在**状态首次进入 OFFLINE** 时 WARN 一次，
  后续降频探测失败按 DEBUG，不刷屏。
- fail-static 不变：被拒 ≠ 控制面挂了，不清快照、不阻断玩家（玩家照常按本地有效配置运行）；
  `OFFLINE` 与 `DEGRADED`（控制面不可用）严格区分。

### 前端
- `InstancesPage`：下线去掉「必须先在过滤条件选环境」前置校验；下线/取消下线均用**行内 `i.namespace`** 调用。
- `offlineInstance(serverId, namespace, reason?)` 带可选 reason；新增 `onlineInstance(serverId, namespace)`。
- 已下线实例在表里可识别（按需展示「已下线」标记，数据源 `ListOffline`），并提供「取消下线」操作。

## 4. 任务拆分
- [ ] 控制面：`server_offline` 模型 + 仓库 + AutoMigrate + 错误码 + action 枚举。
- [ ] 控制面：`InstanceService` 注册查拒绝表、Offline 落库、Online 清除；handler + 路由。
- [ ] 控制面测试：注册遇拒绝态 403、取消后可注册、与 drain/TTL 不冲突、事务原子、重复 serverId 守卫不破。
- [ ] agent：`RegisterOutcome.OfflineRejected` + `AgentState.OFFLINE` + 降频探测重连 + 降噪。
- [ ] agent 测试：收 403 进 OFFLINE 停猛重连、取消后探测可恢复、与 DEGRADED 区分。
- [ ] 前端：去前置校验、按行 namespace 下线 / 取消下线；client API；测试。
- [ ] 文档同步：PRD 状态、ADR、ARCHITECTURE、API、CHANGELOG。

## 5. 验收标准
- 主动下线某在线实例后：该实例从可用集消失；其 agent 重新注册收到 `INSTANCE_OFFLINE_REJECTED`(403)、进入 `OFFLINE` 态、停止猛重连、不刷错误日志。
- 控制面重启后，被下线实例仍被拒绝接入（DB 持久）。
- 取消下线后，agent 经降频探测 / 运维 reconnect 可重新注册成功、恢复 RUNNING。
- 下线 ≠ drain ≠ lost/offline：drain 的实例仍能注册接入（只是不被落位），TTL 衰退不写 `server_offline`，三者在测试中互不串扰。
- 前端在未选环境时也能按行下线（用行 namespace），并能取消下线。
- 重复 serverId 守卫、fail-static 不被本改动破坏（既有测试全绿）。

## 6. 风险 / 待定
- 心跳不查库：依赖「移出内存→心跳 404→重注册被拒」链路收敛，若未来心跳改为可独立复活实例需重新评估（当前心跳 404 即触发重注册，链路成立）。
- 降频探测间隔取值：默认值需足够大以「不刷屏」，又不至于取消下线后恢复过慢；运维可经 reconnect 命令即时恢复。
- 真机维度（真 agent 被拒后停止重连、取消后恢复）本机无 MC 集群，标「待真机验」。
```
