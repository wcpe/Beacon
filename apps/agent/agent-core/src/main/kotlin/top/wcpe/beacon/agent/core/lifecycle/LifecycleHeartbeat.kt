package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.HeartbeatOutcome
import top.wcpe.beacon.agent.core.client.heartbeat

// ---- 心跳循环 ----

internal fun AgentLifecycle.startHeartbeatLoop() {
    val gen = heartbeatGen.get() + 1
    heartbeatGen.set(gen)
    scheduleHeartbeat(gen, heartbeatIntervalMs)
}

internal fun AgentLifecycle.scheduleHeartbeat(
    gen: Int,
    delayMs: Long,
) {
    if (!running.get()) return
    adapter.runAsyncDelayed(delayMs) { heartbeatTick(gen) }
}

internal fun AgentLifecycle.heartbeatTick(gen: Int) {
    // 代标识不符（已重启循环或已 shutdown）→ 当前跳作废。
    if (!running.get() || gen != heartbeatGen.get()) return
    when (apiClient.heartbeat(identity)) {
        is HeartbeatOutcome.Ok -> scheduleHeartbeat(gen, heartbeatIntervalMs)
        is HeartbeatOutcome.NotRegistered -> {
            adapter.warn("心跳返回未注册，触发重新注册")
            // 重新注册会重启两条循环，本代心跳到此为止；经单飞门，与其它触发点互斥。
            triggerReregister()
        }

        is HeartbeatOutcome.AuthorityRefreshRequired -> {
            adapter.warn("心跳被控制面拒绝，回查 v2 权威身份状态")
            triggerReregister()
        }

        is HeartbeatOutcome.Failed -> {
            // 心跳连接失败不进 DEGRADED（长轮询循环负责连接级降级判定），按周期重试即可。
            scheduleHeartbeat(gen, heartbeatIntervalMs)
        }
    }
}
