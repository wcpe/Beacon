package com.beacon.agent.core.backoff

import com.beacon.agent.core.settings.BackoffSettings
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/** ExponentialBackoff 增长 / 封顶 / 重置的单元测试。 */
class ExponentialBackoffTest {

    private fun settings() = BackoffSettings(
        initialMs = 1000,
        maxMs = 8000,
        multiplier = 2.0,
        jitterRatio = 0.2,
    )

    @Test
    fun `无抖动时按倍率增长`() {
        // jitterSource 固定为 0.5 → jitter = current * 0.2 * (0.5*2-1) = 0，等价无抖动。
        val backoff = ExponentialBackoff(settings(), jitterSource = { 0.5 })
        assertEquals(1000, backoff.nextDelayMs())
        assertEquals(2000, backoff.nextDelayMs())
        assertEquals(4000, backoff.nextDelayMs())
    }

    @Test
    fun `增长封顶于 maxMs`() {
        val backoff = ExponentialBackoff(settings(), jitterSource = { 0.5 })
        // 1000 → 2000 → 4000 → 8000(封顶) → 8000...
        backoff.nextDelayMs() // 1000
        backoff.nextDelayMs() // 2000
        backoff.nextDelayMs() // 4000
        assertEquals(8000, backoff.nextDelayMs())
        assertEquals(8000, backoff.nextDelayMs())
    }

    @Test
    fun `reset 回到初始值`() {
        val backoff = ExponentialBackoff(settings(), jitterSource = { 0.5 })
        backoff.nextDelayMs()
        backoff.nextDelayMs()
        backoff.reset()
        assertEquals(1000, backoff.nextDelayMs())
    }

    @Test
    fun `抖动结果落在 current 的正负 jitterRatio 区间内`() {
        // jitterSource=1.0 → +20%；jitterSource=0.0 → -20%。
        val high = ExponentialBackoff(settings(), jitterSource = { 1.0 }).nextDelayMs()
        val low = ExponentialBackoff(settings(), jitterSource = { 0.0 }).nextDelayMs()
        assertEquals(1200, high) // 1000 + 1000*0.2
        assertEquals(800, low) // 1000 - 1000*0.2
    }

    @Test
    fun `延迟不为负`() {
        // 极端抖动比例下也不应为负。
        val s = BackoffSettings(initialMs = 100, maxMs = 1000, multiplier = 2.0, jitterRatio = 2.0)
        val delay = ExponentialBackoff(s, jitterSource = { 0.0 }).nextDelayMs()
        assertTrue(delay >= 0)
    }
}
