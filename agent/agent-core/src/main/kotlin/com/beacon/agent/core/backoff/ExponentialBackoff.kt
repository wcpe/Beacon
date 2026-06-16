package com.beacon.agent.core.backoff

import com.beacon.agent.core.settings.BackoffSettings

/**
 * 指数退避（带抖动、封顶）。仅在连接级失败时使用；任一成功调用后 reset。
 *
 * 非线程安全：约定只在单一循环线程内推进。jitterSource 便于测试注入确定性抖动。
 */
class ExponentialBackoff(
    private val settings: BackoffSettings,
    private val jitterSource: () -> Double = { Math.random() },
) {

    private var current: Long = settings.initialMs

    /** 计算下次等待并推进；带 ±jitter 抖动避免惊群，结果不超过上限、不小于 0。 */
    fun nextDelayMs(): Long {
        // jitter ∈ [-jitterRatio, +jitterRatio] × current
        val jitter = (current * settings.jitterRatio * (jitterSource() * 2 - 1)).toLong()
        val delay = (current + jitter).coerceIn(0, settings.maxMs)
        // 推进 current，封顶 maxMs。
        current = (current * settings.multiplier).toLong().coerceAtMost(settings.maxMs)
        return delay
    }

    /** 重置到初始值（任一成功后调用）。 */
    fun reset() {
        current = settings.initialMs
    }
}
