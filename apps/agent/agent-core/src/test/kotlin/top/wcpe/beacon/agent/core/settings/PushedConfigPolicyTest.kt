package top.wcpe.beacon.agent.core.settings

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * 下发配置键级防护的测试（ADR-0095）。
 *
 * 这组测试守的是一条**安全边界**，不是普通功能：
 * 若 `beacon.endpoints` 或 `beacon.bootstrap-token` 能被下发，控制面（或被攻破的控制面）
 * 就能把 agent 指向另一个控制面、或用别的身份接入——提权面从「执行命令」扩大到「接管身份」。
 */
class PushedConfigPolicyTest {
    @Test
    fun `接入目标与身份禁止下发`() {
        val forbidden =
            listOf(
                "beacon.endpoints",
                "beacon.bootstrap-token",
                "identity.server-id",
                "identity.identity-id",
                "identity.anything",
            )
        forbidden.forEach {
            assertFalse(PushedConfigPolicy.isPushable(it), "$it 必须禁止下发（否则控制面可劫持 agent）")
        }
    }

    @Test
    fun `可调参数允许下发`() {
        val allowed =
            listOf(
                "delivery.config-reload-timeout-ms",
                "delivery.reload-pool-size",
                "timing.heartbeat-fallback-ms",
                "assets.scan-interval-sec",
                "override.command-whitelist",
            )
        allowed.forEach {
            assertTrue(PushedConfigPolicy.isPushable(it), "$it 应允许下发")
        }
    }

    @Test
    fun `未知键默认放行以支持前向兼容`() {
        // 新控制面下发老 agent 不认识的键时，老 agent 应忽略而不是报错（协议加性）。
        assertTrue(PushedConfigPolicy.isPushable("some.future.key"))
    }

    @Test
    fun `分区把危险键挑出来且不丢失信息`() {
        val (allowed, rejected) =
            PushedConfigPolicy.partition(
                mapOf(
                    "beacon.endpoints" to "http://evil",
                    "delivery.config-reload-timeout-ms" to "30000",
                    "identity.server-id" to "x",
                ),
            )

        assertEquals(mapOf("delivery.config-reload-timeout-ms" to "30000"), allowed)
        // 被拒的键必须能报出来：静默丢弃会让「Web 改了没生效」无从排查。
        assertEquals(setOf("beacon.endpoints", "identity.server-id"), rejected.toSet())
    }

    @Test
    fun `空键不可下发`() {
        assertFalse(PushedConfigPolicy.isPushable(""))
        assertFalse(PushedConfigPolicy.isPushable("   "))
    }

    @Test
    fun `空白包裹的禁区键同样被拒`() {
        // 防止通过前后空格绕过（配置解析若先 trim 就会命中真实键）。
        assertFalse(PushedConfigPolicy.isPushable("  beacon.endpoints  "))
    }
}
