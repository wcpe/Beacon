package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.PollResult
import top.wcpe.beacon.agent.core.client.pollEffective

// ---- 长轮询循环 ----

internal fun AgentLifecycle.startConfigPollLoop() {
    val gen = pollGen.get() + 1
    pollGen.set(gen)
    schedulePoll(gen, 0)
}

internal fun AgentLifecycle.schedulePoll(
    gen: Int,
    delayMs: Long,
) {
    if (!running.get()) return
    if (delayMs <= 0) {
        adapter.runAsync { pollTick(gen) }
    } else {
        adapter.runAsyncDelayed(delayMs) { pollTick(gen) }
    }
}

internal fun AgentLifecycle.pollTick(gen: Int) {
    if (!running.get() || gen != pollGen.get()) return
    val currentMd5 = store.currentMd5()
    when (val result = apiClient.pollEffective(identity, currentMd5, settings.pollTimeoutMs)) {
        is PollResult.Changed -> {
            // 200：apply（含写快照、广播）→ report → 用新 md5 续杯。
            applier.apply(result.effective)
            reportApplied(result.effective.md5)
            markRunningAfterPollSuccess()
            schedulePoll(gen, 0)
        }

        is PollResult.NotModified -> {
            // 304：用旧 md5 立即续杯，不退避。
            markRunningAfterPollSuccess()
            schedulePoll(gen, 0)
        }

        is PollResult.NotRegistered -> {
            adapter.warn("长轮询返回未注册，触发重新注册")
            triggerReregister()
        }

        is PollResult.Failed -> {
            // 连接级失败：进 DEGRADED，保持当前有效配置不回退，退避后重连。
            state.set(AgentState.DEGRADED)
            val delay = pollBackoff.nextDelayMs()
            adapter.warn("长轮询连接失败（${result.reason}），保持当前有效配置，${delay}ms 后重连")
            schedulePoll(gen, delay)
        }
    }
}

/** 长轮询成功一轮：重置退避并回到 RUNNING（从 DEGRADED 恢复）。 */
internal fun AgentLifecycle.markRunningAfterPollSuccess() {
    pollBackoff.reset()
    if (state.get() == AgentState.DEGRADED) {
        state.set(AgentState.RUNNING)
        adapter.info("已重连控制面，恢复 RUNNING")
    }
}

/**
 * config-changed：以当前本地 md5 拉一次 config/effective（服务端 md5 已变 → 立即 200，不挂起），apply 并 report。
 * 用 ConfigApplier 的 md5 幂等守卫兜底重复事件；404 触发重新注册。
 */
internal fun AgentLifecycle.fetchAndApplyConfigOnce() {
    when (val result = apiClient.pollEffective(identity, store.currentMd5(), settings.requestTimeoutMs)) {
        is PollResult.Changed -> {
            applier.apply(result.effective)
            reportApplied(result.effective.md5)
            markRunningAfterStreamSuccess()
        }

        is PollResult.NotModified -> markRunningAfterStreamSuccess() // 已是最新（重复事件），无害
        is PollResult.NotRegistered -> {
            adapter.warn("SSE 取配置返回未注册，触发重新注册")
            triggerReregister()
        }

        is PollResult.Failed -> adapter.warn("SSE 取配置失败（${result.reason}），保持当前有效配置，待下次事件/重连")
    }
}
