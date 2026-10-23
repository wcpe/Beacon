package top.wcpe.beacon.agent.core.identity

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertIs
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * `/beacon join` 决策的单测（FR-276）。
 *
 * ## 为什么这组用例必须存在
 *
 * FR-276 的验收口径明确要求「在已绑定实例上再次执行 `/beacon join` 被明确拒绝」，
 * 并要求「必须有测试钉住」。该判断原先埋在两份 `BeaconJoinCommand` 的 private 方法里、
 * 依赖 TabooLib 的 `ProxyCommandSender`，**无法被任何测试触达**——没有测试时，
 * 有人把这段守卫删掉或改坏也不会有任何信号。
 *
 * 现在决策收敛为 [JoinDecision] 纯函数，本组用例即钉住该守卫。
 */
class JoinDecisionTest {
    private fun bound() =
        AgentCredentials.BoundIdentity(
            namespace = "mc-prod",
            serverId = "game-01",
            kind = "backend",
            address = "127.0.0.1:25565",
            boundAt = null,
        )

    private fun existingWithBinding() =
        AgentCredentials(
            identityId = "11111111-1111-4111-8111-111111111111",
            endpoint = "http://127.0.0.1:20020",
            token = "bn_old",
            binding = bound(),
        )

    /** 核心用例：已绑定实例再次 join 必须被拒绝，且不产生任何待写入凭据。 */
    @Test
    fun `已绑定的实例再次 join 被明确拒绝`() {
        val decision =
            JoinDecision.decide(
                existing = existingWithBinding(),
                args = listOf("http://attacker.example.com:20020", "bn_new"),
                newIdentityId = { error("已绑定分支不应生成新身份") },
            )

        val rejected = assertIs<JoinDecision.AlreadyBound>(decision, "已绑定实例必须被拒绝，实际=$decision")
        // 回显要能指出当前归属，否则运维不知道该找谁解绑。
        assertEquals("mc-prod", rejected.namespace)
        assertEquals("game-01", rejected.serverId)
    }

    /** 拒绝时必须带出处方可读的提示，不是静默返回。 */
    @Test
    fun `拒绝文案指出当前归属与正确出口`() {
        val message = JoinDecision.alreadyBoundMessage("mc-prod", "game-01")

        assertTrue(message.contains("mc-prod/game-01"), "应指出当前绑定：$message")
        assertTrue(message.contains("不通过 join 改指向"), "应说明不通过 join 改指向：$message")
        assertTrue(message.contains("解绑"), "应给出正确出口（解绑）：$message")
    }

    /**
     * 已绑定优先于地址校验：哪怕地址写错，也应报「已绑定」而不是「地址格式不对」。
     *
     * 顺序反了会误导运维——以为改对地址就能重接，而真实原因是本机已归属。
     */
    @Test
    fun `已绑定优先于地址校验上报`() {
        val decision =
            JoinDecision.decide(
                existing = existingWithBinding(),
                args = listOf("not-a-url", "bn_new"),
                newIdentityId = { error("不应生成新身份") },
            )

        assertIs<JoinDecision.AlreadyBound>(decision, "已绑定时应先报归属而不是地址格式")
    }

    /** 未绑定（binding 为 null）时照常放行，且沿用既有身份标识。 */
    @Test
    fun `未绑定时放行并沿用既有身份`() {
        val existing =
            AgentCredentials(
                identityId = "22222222-2222-4222-8222-222222222222",
                endpoint = "",
                token = "",
                binding = null,
            )

        val decision =
            JoinDecision.decide(
                existing = existing,
                args = listOf("http://127.0.0.1:20020", "bn_new"),
                newIdentityId = { error("既有身份非空时不应重新生成") },
            )

        val proceed = assertIs<JoinDecision.Proceed>(decision)
        assertEquals("22222222-2222-4222-8222-222222222222", proceed.credentials.identityId)
        assertEquals("http://127.0.0.1:20020", proceed.credentials.endpoint)
        assertEquals("bn_new", proceed.credentials.token)
        // 未绑定路径不得凭空造出绑定事实。
        assertNull(proceed.credentials.binding, "放行路径不得写入 binding")
    }

    /** 首次接入（无凭据）时生成新身份。 */
    @Test
    fun `首次接入生成新身份`() {
        var generated = 0
        val decision =
            JoinDecision.decide(
                existing = null,
                args = listOf("https://beacon.example.com", "bn_new"),
                newIdentityId = {
                    generated++
                    "33333333-3333-4333-8333-333333333333"
                },
            )

        val proceed = assertIs<JoinDecision.Proceed>(decision)
        assertEquals("33333333-3333-4333-8333-333333333333", proceed.credentials.identityId)
        assertEquals(1, generated, "应恰好生成一次身份")
    }

    /** 参数不足回用法，且不生成身份。 */
    @Test
    fun `参数不足时不生成身份`() {
        val decision =
            JoinDecision.decide(
                existing = null,
                args = listOf("http://127.0.0.1:20020"),
                newIdentityId = { error("参数不足不应生成身份") },
            )

        assertIs<JoinDecision.MissingArgs>(decision)
    }

    /** 地址形态非法被拒。 */
    @Test
    fun `非 http 地址被拒`() {
        val decision =
            JoinDecision.decide(
                existing = null,
                args = listOf("127.0.0.1:20020", "bn_new"),
                newIdentityId = { error("地址非法不应生成身份") },
            )

        assertIs<JoinDecision.InvalidEndpoint>(decision)
    }

    /** 地址形态校验只认 http/https（大小写不敏感）。 */
    @Test
    fun `地址形态校验的边界`() {
        assertTrue(JoinDecision.looksLikeEndpoint("http://a"))
        assertTrue(JoinDecision.looksLikeEndpoint("HTTPS://a"))
        assertFalse(JoinDecision.looksLikeEndpoint("ftp://a"))
        assertFalse(JoinDecision.looksLikeEndpoint("a.example.com"))
    }
}
