package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.DeclarationOutcome
import top.wcpe.beacon.agent.api.NodeDeclaration
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.testutil.CapturingCodec
import top.wcpe.beacon.agent.core.testutil.FakeHttpTransport
import top.wcpe.beacon.agent.core.testutil.declarationIdentity
import top.wcpe.beacon.agent.core.testutil.declarationSettings
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * SelfDeclarationView 单测（FR-243，见 ADR-0086 决策 3）：core 调用结果 → 门面三档取值的映射。
 *
 * 重点：三类结论取值互斥（同一个返回值绝不承载两类失败）；拒绝 / 不可用各记一行脱敏告警；
 * 门面「绝不外抛」在空声明与实现异常下都成立。
 */
class SelfDeclarationViewTest {
    private fun view(
        transport: FakeHttpTransport,
        codec: CapturingCodec = CapturingCodec(),
        warns: MutableList<String> = mutableListOf(),
    ): SelfDeclarationView =
        SelfDeclarationView(BeaconApiClient(transport, codec, declarationSettings()), declarationIdentity(), warns::add)

    @Test
    fun `200 映射为 APPLIED 并回带生效值`() {
        val codec = CapturingCodec(mapOf("ok" to true, "capacity" to 256, "labels" to mapOf("mode" to "canary")))
        val outcome = view(FakeHttpTransport(status = 200, body = "applied"), codec).declare(NodeDeclaration.ofCapacity(256))

        assertEquals(DeclarationOutcome.Status.APPLIED, outcome.status())
        assertEquals(256, outcome.applied().get().capacity().orElse(null))
        assertEquals(mapOf("mode" to "canary"), outcome.applied().get().labels())
        assertFalse(outcome.rejectReason().isPresent, "APPLIED 不得携带拒绝原因（两类失败不共用一个返回值）")
    }

    @Test
    fun `400 映射为 REJECTED 并带原因且记告警`() {
        val warns = mutableListOf<String>()
        val codec = CapturingCodec(mapOf("code" to "INVALID_PARAM"))
        val outcome =
            view(FakeHttpTransport(status = 400, body = "rejected"), codec, warns)
                .declare(NodeDeclaration.ofLabels(mapOf("k" to "v")))

        assertEquals(DeclarationOutcome.Status.REJECTED, outcome.status())
        assertEquals("INVALID_PARAM", outcome.rejectReason().get())
        assertFalse(outcome.applied().isPresent, "REJECTED 不得携带生效值")
        assertTrue(warns.any { it.contains("被拒") && it.contains("INVALID_PARAM") }, "拒绝须让运维看得见：$warns")
    }

    @Test
    fun `401 与 404 映射为 UNAVAILABLE 且不带拒绝原因`() {
        for (status in listOf(401, 404)) {
            val outcome =
                view(FakeHttpTransport(status = status)).declare(NodeDeclaration.ofCapacity(1))

            assertEquals(DeclarationOutcome.Status.UNAVAILABLE, outcome.status(), "状态码 $status 应为通道不可用")
            assertFalse(outcome.rejectReason().isPresent, "通道不可用不等于「声明被拒」")
            assertFalse(outcome.applied().isPresent)
        }
    }

    @Test
    fun `连接失败映射为 UNAVAILABLE 并记告警`() {
        val warns = mutableListOf<String>()
        val transport = FakeHttpTransport().apply { down = true }
        val outcome = view(transport, CapturingCodec(), warns).declare(NodeDeclaration.ofCapacity(1))

        assertEquals(DeclarationOutcome.Status.UNAVAILABLE, outcome.status())
        assertTrue(warns.any { it.contains("通道不可用") }, "不可用原因须落日志：$warns")
    }

    @Test
    fun `空声明判 REJECTED 且不发起请求`() {
        val transport = FakeHttpTransport()
        val outcome = view(transport).declare(null)

        assertEquals(DeclarationOutcome.Status.REJECTED, outcome.status())
        assertTrue(outcome.rejectReason().isPresent)
        assertEquals(0, transport.calls.get(), "空声明不应发起请求")
    }

    @Test
    fun `传输层抛异常时也降级为 UNAVAILABLE 而不外抛`() {
        val transport =
            object : HttpTransport {
                override fun execute(request: HttpRequest): HttpResponse = throw RuntimeException("模拟不可预期异常")
            }
        val outcome =
            SelfDeclarationView(
                BeaconApiClient(transport, CapturingCodec(), declarationSettings()),
                declarationIdentity(),
            ).declare(NodeDeclaration.ofCapacity(1))

        assertEquals(DeclarationOutcome.Status.UNAVAILABLE, outcome.status(), "连接级异常应转通道不可用，绝不外抛")
    }
}
