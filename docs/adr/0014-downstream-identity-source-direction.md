# ADR-0014：下游身份来源方向——优先取自 Beacon agent，本地降级

**状态**：已接受

## 背景

`docs/SDK.md` 原纪律假定身份方向为「身份/zone 走 **CoreLib**，Beacon SDK 只读配置 + 查发现，`identity()` 仅薄转发」——即把 CoreLib 当作 serverId/zoneId 的真源、Beacon SDK 是消费者。

实际部署诉求相反：希望由 CoreLib（及其下游业务）**统一从 Beacon 取 serverId/zoneId**，仅在 Beacon agent 不在场时回退 CoreLib 本地 `config.yml`。zone 本就是控制面 DB 权威指派的（[ADR-0004](0004-zone-authority-control-plane.md)），让 CoreLib 直接消费权威值更顺；serverId 若两处各配则易漂移。需要把这个方向定下来，并解决 zone「异步注册才回填」带来的启动时序问题——CoreLib 在 `LifeCycle.ENABLE` 同步初始化身份，而 agent 的 zone 要等注册响应才有，且 SDK 原本没有「身份就绪」通知。

## 决策

1. **下游身份来源方向反转**：agent 在场时，下游（CoreLib）的 serverId 与 zone 均取自 Beacon agent（`BeaconAccess.identity()`）；agent **不在场**时回退 CoreLib 本地 `config.yml` 并打 **WARN 降级警告**。
2. **不改变 [ADR-0004](0004-zone-authority-control-plane.md)**：agent 仍自管 serverId（本地 bootstrap 声明并上报）、zone 仍由控制面 DB 权威指派。本 ADR 只规定「下游消费身份的来源方向」——serverId 的本地真源从 CoreLib `config.yml` 迁移到 **agent `config.yml`**（agent 在场时），CoreLib 本地 `config.yml` 的 `server.id` / `server.zone-id` 降为 **agent 不在场时的兜底**。
3. **agent 在场则必须取得确切身份才放行启动**（确定身份优先于可用性）：
   - 经新增的就绪原语（`BeaconAgent.awaitRegistered` / `BeaconAccess.awaitIdentity`）等待首次注册完成。CoreLib 采取「**持续等待直到就绪**」——**不因超时降级本地**：没有确定身份不应开服，宁可阻塞启动也不用残缺/可能过时的身份（控制面恢复、注册成功后自然放行）。
   - 注册完成后若 zone 仍**未指派**（控制面未给该服指派 zone）→ 视为身份不完整，打 **ERROR 并中止/关闭服务器**，**绝不用本地兜底**；要求运维先在 Beacon 指派 zone 再开服。
   - 即「agent 在场 = 这台服已纳入 Beacon 统一身份管理，serverId 与 zone 都必须由控制面给全」。
4. **降级只发生在 agent 不在场**（`isBeaconPresent()`=false，含未安装 BeaconAgent 插件——下游须先用平台 API 探测插件是否在场，再碰 SDK 类，避免 `NoClassDefFoundError`）：此时回退本地 `config.yml` + WARN。在场后**没有超时降级路径**。
5. `awaitRegistered(timeoutMillis)` 是通用有界等待原语（超时返回 false）；CoreLib 以极大超时实现「实际无限等待」。

## 理由

- **单一身份真源**：serverId 只配在 agent 一处，消除 CoreLib 与 agent 两处各配导致的漂移；zone 直接用控制面权威值，免去逐节点配 zone（正是 ADR-0004 集中指派的收益）。
- **确定身份优先于可用性**：CoreLib 的 zoneId 用于数据库分区隔离，身份残缺/错误会导致**数据串区**，比「启动被阻塞」危险得多。故 agent 在场时强制取得控制面权威的完整身份（serverId + zone），取不到宁可不开服。
- **fail-static 的边界**：fail-static 针对「agent 已就绪后控制面短暂抖动」——那时用本地快照继续；而「启动期尚未取得首份确定身份」不属于 fail-static，必须先拿到。

## 后果

- 控制面长期不可用且 agent 在场时，CoreLib 启动会**阻塞**直到控制面恢复、注册成功——有意取舍（确定身份优先）。
- zone 未指派会**阻止服务器启动**，运维须先在 Beacon 指派 zone。
- `docs/SDK.md` 的「身份走 CoreLib」纪律由本 ADR **修订/取代**（SDK.md 随本 ADR 同步改写）。
- **部署规范变化**：serverId 配在 agent `config.yml`（在场真源），CoreLib `config.yml` 的 `server.id` / `server.zone-id` 仅作 agent 不在场时的兜底。

## 备选方案

- **就绪等待设超时、超时降级本地**：启动不被长期阻塞，但可能用残缺/过时身份开服 → 数据串区风险。**被否**（确定身份优先）。
- **zone 未指派时用本地 zone-id 兜底**：同样有串区风险、且掩盖运维漏指派。**被否**（改为中止启动）。
- **不等待，本地兜底 + 异步刷新**：ZoneInterceptor 实时读 zoneId，刷新前的 DB 读写会用错分区 → 串区窗口。**被否**。
- **维持原方向（CoreLib 自管身份）**：serverId/zone 两处各配易漂移、zone 要逐节点配，违背 ADR-0004 集中指派的收益。**被否**。
