package top.wcpe.beacon.agent.adapters

import top.wcpe.beacon.agent.adapters.testutil.FakeHttpTransport
import top.wcpe.beacon.agent.adapters.testutil.TestFixtures
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.command.AgentCommand
import top.wcpe.beacon.agent.core.delivery.DeliveryStageReport
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.transport.HttpResponse
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNotNull
import kotlin.test.assertTrue

/**
 * BeaconApiClient 交付面方法真 JSON 契约单测（FR-165，spec §5.2）：校验对控制面 upload-manifest / manifest
 * 响应的解析与 result 回执报文的字段名 / 值（用真 [KotlinxJsonCodec]，锁定与控制面 camelCase 契约一致）。
 */
class BeaconApiClientDeliveryTest {
    private val codec = KotlinxJsonCodec()

    private fun identity(): AgentIdentity = TestFixtures.identity().copy(identityId = "id-x", bootId = "boot-x")

    @Test
    fun `解析待上传清单 upload-manifest`() {
        val transport =
            FakeHttpTransport().enqueue(
                HttpResponse(200, """{"orderId":7,"items":[{"path":"plugins/a.jar","sha256":"abc123","size":123}]}"""),
            )
        val client = BeaconApiClient(transport, codec, TestFixtures.settings())

        val manifest = assertNotNull(client.fetchDeliveryUploadManifest(identity(), 7L))

        assertEquals(7L, manifest.orderId)
        assertEquals(1, manifest.items.size)
        assertEquals("plugins/a.jar", manifest.items[0].path)
        assertEquals("abc123", manifest.items[0].sha256)
        assertEquals(123L, manifest.items[0].sizeBytes)
        assertTrue(transport.captured[0].url.endsWith("/beacon/v2/agent/delivery/orders/7/upload-manifest"))
    }

    @Test
    fun `解析目标差异清单 manifest delete 项无 sha 与 size`() {
        val body =
            """{"orderId":7,"activationMethod":"restart","files":[
               {"path":"plugins/a.jar","action":"update","sha256":"abc","size":123},
               {"path":"plugins/old.txt","action":"delete"}],
               "configs":[{"scopeKind":"server","scopeId":3,"fromVersionId":null,"toVersionId":5}]}"""
        val transport = FakeHttpTransport().enqueue(HttpResponse(200, body))
        val client = BeaconApiClient(transport, codec, TestFixtures.settings())

        val manifest = assertNotNull(client.fetchDeliveryManifest(identity(), 7L))

        assertEquals("restart", manifest.activationMethod)
        assertEquals(2, manifest.files.size)
        val update = manifest.files.first { it.path == "plugins/a.jar" }
        assertEquals("update", update.action)
        assertEquals("abc", update.sha256)
        assertEquals(123L, update.sizeBytes)
        val delete = manifest.files.first { it.path == "plugins/old.txt" }
        assertEquals("delete", delete.action)
        assertEquals("", delete.sha256, "delete 项无 sha256（omitempty）应解析为空串")
        assertEquals(0L, delete.sizeBytes)
    }

    @Test
    fun `回执结果 result 报文字段与鉴权头正确`() {
        val transport = FakeHttpTransport().enqueue(HttpResponse(204, ""))
        val client = BeaconApiClient(transport, codec, TestFixtures.settings())

        val ok =
            client.postDeliveryResult(
                identity(),
                7L,
                DeliveryStageReport("push", "success", changedFileCount = 3, skippedFileCount = 1, backupPresent = true, error = ""),
            )

        assertTrue(ok)
        val req = transport.captured[0]
        assertEquals("POST", req.method)
        assertTrue(req.url.endsWith("/beacon/v2/agent/delivery/orders/7/result"))
        assertEquals("id-x", req.headers["X-Beacon-Identity"])
        assertEquals("boot-x", req.headers["X-Beacon-Boot"])
        @Suppress("UNCHECKED_CAST")
        val sent = codec.decode(req.body ?: "") as Map<String, Any?>
        assertEquals("push", sent["phase"])
        assertEquals("success", sent["status"])
        assertEquals(3L, sent["changedFileCount"])
        assertEquals(1L, sent["skippedFileCount"])
        assertEquals(true, sent["backupPresent"])
    }

    @Test
    fun `解析 delivery_activate 命令携 activationMethod 与 orderId`() {
        // 控制面 delivery_activate 命令载荷（camelCase，见 server deliveryActivatePayload）：orderId + activationMethod (+ restart 超时)。
        val transport =
            FakeHttpTransport().enqueue(
                HttpResponse(
                    200,
                    """{"id":9,"type":"delivery_activate","payload":{"orderId":7,"activationMethod":"restart","activateTimeoutSec":300}}""",
                ),
            )
        val client = BeaconApiClient(transport, codec, TestFixtures.settings())

        val cmd = assertNotNull(client.fetchPendingCommand(identity()))

        assertEquals(AgentCommand.TYPE_DELIVERY_ACTIVATE, cmd.type)
        assertEquals(7L, cmd.deliveryPayload?.orderId)
        assertEquals("restart", cmd.deliveryPayload?.activationMethod, "activate 命令应解析出生效方式供 M4 分派")
    }

    @Test
    fun `非 200 的清单响应返回 null`() {
        val transport = FakeHttpTransport().enqueue(HttpResponse(403, """{"code":"DELIVERY_NOT_SOURCE"}"""))
        val client = BeaconApiClient(transport, codec, TestFixtures.settings())

        assertEquals(null, client.fetchDeliveryUploadManifest(identity(), 7L))
    }
}
