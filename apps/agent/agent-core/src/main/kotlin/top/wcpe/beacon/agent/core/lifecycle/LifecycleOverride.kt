package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.OverridePollResult
import top.wcpe.beacon.agent.core.client.pollOverrideSets

// ---- 三方覆盖集长轮询循环（FR-15，与配置 / 文件树长轮询并行、md5 维度独立） ----

/** 启动覆盖集长轮询循环；未启用覆盖集接线（overrideApplier 为 null）则不启。 */
internal fun AgentLifecycle.startOverridePollLoop() {
    if (overrideApplier == null) return
    val gen = overrideGen.get() + 1
    overrideGen.set(gen)
    scheduleOverridePoll(gen, 0)
}

internal fun AgentLifecycle.scheduleOverridePoll(
    gen: Int,
    delayMs: Long,
) {
    if (!running.get()) return
    if (delayMs <= 0) {
        adapter.runAsync { overrideTick(gen) }
    } else {
        adapter.runAsyncDelayed(delayMs) { overrideTick(gen) }
    }
}

internal fun AgentLifecycle.overrideTick(gen: Int) {
    val applierLocal = overrideApplier ?: return
    if (!running.get() || gen != overrideGen.get()) return
    // 当前 overrideMd5 取本地已收敛那一版（首启 / 上轮有集失败则为 null，强制重拉重做）。
    val currentMd5 = applierLocal.currentOverrideMd5()
    when (val result = apiClient.pollOverrideSets(identity, currentMd5, settings.pollTimeoutMs)) {
        is OverridePollResult.Changed -> {
            // 200：逐集落 targetRoot（备份 + 安全校验 + 受管标记）→ 命中白名单才派发重载命令。
            // fail-static 由 applier 内部把控（取内容失败 / 恶意 targetRoot 不动既有、不派发）。
            applierLocal.apply(result.manifest)
            overrideBackoff.reset()
            scheduleOverridePoll(gen, 0)
        }

        is OverridePollResult.NotModified -> {
            // 304：用旧 overrideMd5 立即续杯，不退避。
            overrideBackoff.reset()
            scheduleOverridePoll(gen, 0)
        }

        is OverridePollResult.NotRegistered -> {
            adapter.warn("覆盖集长轮询返回未注册，触发重新注册")
            triggerReregister()
        }

        is OverridePollResult.Failed -> {
            // 连接级失败：fail-static——不动任何已落盘文件、不派发命令，退避后重连。
            val delay = overrideBackoff.nextDelayMs()
            adapter.warn("覆盖集长轮询连接失败（${result.reason}），保留本地覆盖不动，${delay}ms 后重连")
            scheduleOverridePoll(gen, delay)
        }
    }
}

/** override-changed：以当前本地 overrideMd5 拉一次 override-sets 并落盘（fail-static 由 applier 内部把控）。 */
internal fun AgentLifecycle.fetchAndApplyOverrideOnce() {
    val applierLocal = overrideApplier ?: return
    when (val result = apiClient.pollOverrideSets(identity, applierLocal.currentOverrideMd5(), settings.requestTimeoutMs)) {
        is OverridePollResult.Changed -> {
            applierLocal.apply(result.manifest)
            markRunningAfterStreamSuccess()
        }

        is OverridePollResult.NotModified -> markRunningAfterStreamSuccess()
        is OverridePollResult.NotRegistered -> {
            adapter.warn("SSE 取覆盖集返回未注册，触发重新注册")
            triggerReregister()
        }

        is OverridePollResult.Failed -> adapter.warn("SSE 取覆盖集失败（${result.reason}），保留本地覆盖不动，待下次事件/重连")
    }
}
