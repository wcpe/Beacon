package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.api.NodeDeclaration
import top.wcpe.beacon.agent.core.testutil.CapturingCodec
import top.wcpe.beacon.agent.core.testutil.FakeHttpTransport
import top.wcpe.beacon.agent.core.testutil.declarationIdentity
import top.wcpe.beacon.agent.core.testutil.declarationSettings
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertIs
import kotlin.test.assertTrue

/**
 * BeaconApiClient.declare 的报文契约与结果分档单测（FR-243，见 ADR-0086 §2 / 规格 §3.1、§3.4）。
 *
 * 重点：缺键 = 不刷新（capacity 用指针惯例、labels 用「是否提供」区分，空对象 = 清空）；
 * 状态码分档 200/400/401/404 与连接失败各自落到可区分的结论上。
 */
class BeaconApiClientDeclarationTest {
    private fun client(
        transport: FakeHttpTransport,
        codec: CapturingCodec = CapturingCodec(),
    ): BeaconApiClient = BeaconApiClient(transport, codec, declarationSettings())

    @Suppress("UNCHECKED_CAST")
    private fun lastBody(codec: CapturingCodec): Map<String, Any?> = codec.lastEncoded.get() as Map<String, Any?>

    // ---- 报文契约：部分刷新语义（缺键 = 不刷新） ----

    @Test
    fun `只给 capacity 时报文只带 capacity 键且不带 labels`() {
        val codec = CapturingCodec()
        client(FakeHttpTransport(), codec).declare(declarationIdentity(), NodeDeclaration.ofCapacity(100))

        val body = lastBody(codec)
        assertEquals(100, body["capacity"])
        assertFalse(body.containsKey("labels"), "未提供 labels 时不应拼入 labels 键（缺键 = 不刷新）")
        assertEquals(setOf("namespace", "serverId", "capacity"), body.keys, "报文键集合必须与契约一致")
    }

    @Test
    fun `capacity 为 0 时仍拼入报文（与缺键区分）`() {
        val codec = CapturingCodec()
        client(FakeHttpTransport(), codec).declare(declarationIdentity(), NodeDeclaration.ofCapacity(0))

        val body = lastBody(codec)
        assertEquals(0, body["capacity"], "capacity=0 是合法值，必须与「缺键 = 不刷新」区分")
    }

    @Test
    fun `只给 labels 时报文只带 labels 键`() {
        val codec = CapturingCodec()
        client(FakeHttpTransport(), codec)
            .declare(declarationIdentity(), NodeDeclaration.ofLabels(mapOf("mode" to "beta", "zone" to "z1")))

        val body = lastBody(codec)
        assertFalse(body.containsKey("capacity"), "未提供 capacity 时不应拼入 capacity 键（缺键 = 不刷新）")
        assertEquals(mapOf("mode" to "beta", "zone" to "z1"), body["labels"])
        assertEquals(setOf("namespace", "serverId", "labels"), body.keys, "报文键集合必须与契约一致")
    }

    @Test
    fun `提供空 labels 时拼入空对象（清空全部标签）`() {
        val codec = CapturingCodec()
        client(FakeHttpTransport(), codec).declare(declarationIdentity(), NodeDeclaration.ofLabels(emptyMap()))

        val body = lastBody(codec)
        assertTrue(body.containsKey("labels"), "提供即整体替换：空对象必须拼入，表示清空")
        assertEquals(emptyMap<String, String>(), body["labels"])
    }

    @Test
    fun `两者都给时报文同时带 capacity 与 labels`() {
        val codec = CapturingCodec()
        client(FakeHttpTransport(), codec)
            .declare(declarationIdentity(), NodeDeclaration.of(64, mapOf("mode" to "beta")))

        val body = lastBody(codec)
        assertEquals(64, body["capacity"])
        assertEquals(mapOf("mode" to "beta"), body["labels"])
    }

    @Test
    fun `声明请求打在新端点并带鉴权头`() {
        val transport = FakeHttpTransport()
        client(transport).declare(declarationIdentity(), NodeDeclaration.ofCapacity(1))

        val request = transport.lastRequest.get()
        assertEquals("http://localhost:8848/beacon/v1/agent/declaration", request?.url, "必须打在既有 agent 组的声明端点")
        assertEquals("POST", request?.method)
        assertEquals("tk", request?.headers?.get(BeaconApiClient.HEADER_TOKEN), "身份/鉴权沿用既有头")
        assertEquals("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", request?.headers?.get(BeaconApiClient.HEADER_BOOT))
    }

    // ---- 结果分档 ----

    @Test
    fun `200 时判已生效并回带生效值`() {
        val transport = FakeHttpTransport(status = 200, body = "applied")
        val codec = CapturingCodec(mapOf("ok" to true, "capacity" to 128, "labels" to mapOf("mode" to "beta")))
        val result = client(transport, codec).declare(declarationIdentity(), NodeDeclaration.ofCapacity(128))

        val applied = assertIs<DeclarationResult.Applied>(result, "200 应为已生效")
        assertEquals(128, applied.applied.capacity().orElse(null))
        assertEquals(mapOf("mode" to "beta"), applied.applied.labels())
    }

    @Test
    fun `生效值为 0 也能回带（不与缺键混淆）`() {
        val transport = FakeHttpTransport(status = 200, body = "applied")
        val codec = CapturingCodec(mapOf("ok" to true, "capacity" to 0, "labels" to emptyMap<String, String>()))
        val result = client(transport, codec).declare(declarationIdentity(), NodeDeclaration.ofCapacity(0))

        val applied = assertIs<DeclarationResult.Applied>(result)
        assertEquals(0, applied.applied.capacity().orElse(null))
        assertTrue(applied.applied.labels().isEmpty())
    }

    @Test
    fun `400 时判声明被拒并带回原因`() {
        val transport = FakeHttpTransport(status = 400, body = "rejected")
        val codec = CapturingCodec(mapOf("code" to "INVALID_PARAM"))
        val result = client(transport, codec).declare(declarationIdentity(), NodeDeclaration.ofLabels(mapOf("k" to "v")))

        val rejected = assertIs<DeclarationResult.Rejected>(result, "400 应为声明被拒（稳定事实）")
        assertEquals("INVALID_PARAM", rejected.reason)
    }

    @Test
    fun `400 缺原因码时回退到脱敏消息`() {
        val transport = FakeHttpTransport(status = 400, body = "rejected")
        val codec = CapturingCodec(mapOf("message" to "标签数量超界"))
        val result = client(transport, codec).declare(declarationIdentity(), NodeDeclaration.ofCapacity(1))

        val rejected = assertIs<DeclarationResult.Rejected>(result)
        assertEquals("标签数量超界", rejected.reason)
    }

    @Test
    fun `401 与 404 均判通道不可用（可重试）`() {
        for (status in listOf(401, 404)) {
            val transport = FakeHttpTransport(status = status)
            val result = client(transport).declare(declarationIdentity(), NodeDeclaration.ofCapacity(1))

            assertIs<DeclarationResult.Unavailable>(result, "状态码 $status 应判通道不可用而非被拒")
        }
    }

    @Test
    fun `连接失败判通道不可用`() {
        val transport = FakeHttpTransport().apply { down = true }
        val result = client(transport).declare(declarationIdentity(), NodeDeclaration.ofCapacity(1))

        assertIs<DeclarationResult.Unavailable>(result, "连接失败应判通道不可用（可重试）")
    }

    @Test
    fun `非预期状态码判通道不可用而非被拒`() {
        val transport = FakeHttpTransport(status = 500)
        val result = client(transport).declare(declarationIdentity(), NodeDeclaration.ofCapacity(1))

        assertIs<DeclarationResult.Unavailable>(result, "非预期状态码不得当成「声明被拒」这一稳定事实")
    }
}
