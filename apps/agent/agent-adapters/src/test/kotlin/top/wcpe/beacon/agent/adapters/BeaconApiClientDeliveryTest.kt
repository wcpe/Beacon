package top.wcpe.beacon.agent.adapters

import top.wcpe.beacon.agent.adapters.testutil.FakeHttpTransport
import top.wcpe.beacon.agent.adapters.testutil.TestFixtures
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.HTTP_NOT_SENT
import top.wcpe.beacon.agent.core.client.fetchDeliveryManifest
import top.wcpe.beacon.agent.core.client.fetchDeliveryUploadManifest
import top.wcpe.beacon.agent.core.client.fetchPendingCommand
import top.wcpe.beacon.agent.core.client.postDeliveryResult
import top.wcpe.beacon.agent.core.command.AgentCommand
import top.wcpe.beacon.agent.core.delivery.DeliveryStageReport
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNotNull
import kotlin.test.assertTrue

/**
 * BeaconApiClient 交付面方法真 JSON 契约单测（FR-165，spec §5.2；FR-269 错误码保留）：校验对控制面
 * upload-manifest / manifest 响应的解析、非 200 时的状态码与错误码保留（不再折叠为无信息 null），
 * 与 result 回执报文的字段名 / 值（用真 [KotlinxJsonCodec]，锁定与控制面 camelCase 契约一致）。
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

        val manifest = assertNotNull(client.fetchDeliveryUploadManifest(identity(), 7L).value)

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

        val manifest = assertNotNull(client.fetchDeliveryManifest(identity(), 7L).value)

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
    fun `非 200 的清单响应保留状态码与错误码`() {
        val transport =
            FakeHttpTransport().enqueue(
                HttpResponse(409, """{"code":"config_artifact_missing","message":"配置渲染工件未就绪（payload 未准备）"}"""),
            )
        val client = BeaconApiClient(transport, codec, TestFixtures.settings())

        val result = client.fetchDeliveryUploadManifest(identity(), 7L)

        assertEquals(null, result.value, "非 200 不得给出清单")
        assertEquals(409, result.statusCode)
        assertTrue(result.error.contains("HTTP 409"), "失败摘要应保留状态码：${result.error}")
        assertTrue(result.error.contains("config_artifact_missing"), "失败摘要应保留控制面错误码：${result.error}")
        assertTrue(result.error.contains("配置渲染工件未就绪"), "失败摘要应保留可读说明：${result.error}")
    }

    @Test
    fun `连接失败时清单结果为请求未发出`() {
        // 连接级异常由客户端统一吞为 null，此处注入一个必抛的传输以覆盖该分支。
        val transport =
            object : HttpTransport {
                override fun execute(request: HttpRequest): HttpResponse = throw RuntimeException("连接被拒")
            }
        val client = BeaconApiClient(transport, codec, TestFixtures.settings())

        val result = client.fetchDeliveryManifest(identity(), 7L)

        assertEquals(null, result.value)
        assertEquals(HTTP_NOT_SENT, result.statusCode)
        assertTrue(result.error.contains("连接被拒"), "连接级失败也应带具体原因，便于诊断：${result.error}")
    }
}
