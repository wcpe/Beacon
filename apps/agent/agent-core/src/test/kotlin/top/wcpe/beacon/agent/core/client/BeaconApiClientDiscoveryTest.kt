package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.BackoffSettings
import top.wcpe.beacon.agent.core.settings.FileTreeSettings
import top.wcpe.beacon.agent.core.settings.OverrideSettings
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertIs
import kotlin.test.assertTrue

class BeaconApiClientDiscoveryTest {
    @Test
    fun `discoverResult 200 空列表为权威成功`() {
        val result = client(StatusTransport(200)).discoverResult(filters())

        val success = assertIs<DiscoveryFetchResult.Success<Map<String, Any?>>>(result)
        assertTrue(success.instances.isEmpty())
    }

    @Test
    fun `discoverResult 200 响应结构非法均返回失败`() {
        val invalidTrees =
            listOf(
                emptyList<Any?>(),
                emptyMap<String, Any?>(),
                mapOf("instances" to "not-list"),
                mapOf("instances" to listOf("not-object")),
            )

        invalidTrees.forEach { tree ->
            val result = client(StatusTransport(200), FixedCodec(tree)).discoverResult(filters())

            val failed = assertIs<DiscoveryFetchResult.Failed>(result)
            assertEquals("发现响应结构无效", failed.reason)
        }
    }

    @Test
    fun `discoverResult 解码异常只返回安全分类`() {
        val result = client(StatusTransport(200), ThrowingCodec()).discoverResult(filters())

        val failed = assertIs<DiscoveryFetchResult.Failed>(result)
        assertEquals("发现响应解码失败", failed.reason)
        assertNoSecrets(failed.reason)
    }

    @Test
    fun `discoverResult 连接失败只返回安全分类且旧 discover 仍降级为空列表`() {
        val client = client(ThrowingTransport())

        val result = client.discoverResult(filters())

        val failed = assertIs<DiscoveryFetchResult.Failed>(result)
        assertEquals("发现请求失败", failed.reason)
        assertNoSecrets(failed.reason)
        assertTrue(client.discover(filters()).isEmpty())
    }

    @Test
    fun `discoverResult 非 200 为失败而非权威空列表`() {
        val result = client(StatusTransport(503)).discoverResult(filters())

        val failed = assertIs<DiscoveryFetchResult.Failed>(result)
        assertEquals("非预期状态码 503", failed.reason)
    }

    private fun filters() = DiscoveryFilters(namespace = "prod", group = null, zone = null, role = "bukkit")

    private fun client(
        transport: HttpTransport,
        codec: JsonCodec = EmptyInstancesCodec(),
    ): BeaconApiClient = BeaconApiClient(transport, codec, settings())

    private fun assertNoSecrets(text: String) {
        assertFalse(text.contains("plain-token"))
        assertFalse(text.contains("plain-authorization"))
        assertFalse(text.contains("http://private.example"))
    }

    private fun settings() =
        AgentSettings(
            endpoints = listOf("http://localhost:8848"),
            bootstrapToken = "tk",
            pollTimeoutMs = 50,
            requestTimeoutMs = 200,
            heartbeatFallbackMs = 100_000,
            backoff = BackoffSettings(initialMs = 50, maxMs = 50, multiplier = 1.0, jitterRatio = 0.0),
            snapshotEnabled = false,
            snapshotFileName = "snapshot.json",
            fileTree = FileTreeSettings(enabled = false, targetSubDir = "", appliedManifestFileName = "file-tree.applied.json"),
            override = OverrideSettings(commandWhitelist = emptySet(), backupDirName = "override-backup"),
        )

    private class EmptyInstancesCodec : JsonCodec {
        override fun encode(value: Any?): String = "{}"

        override fun decode(json: String): Any? = mapOf("instances" to emptyList<Any?>())
    }

    private class FixedCodec(private val tree: Any?) : JsonCodec {
        override fun encode(value: Any?): String = "{}"

        override fun decode(json: String): Any? = tree
    }

    private class ThrowingCodec : JsonCodec {
        override fun encode(value: Any?): String = "{}"

        override fun decode(json: String): Any? {
            error("解码失败 token=plain-token Authorization: Bearer plain-authorization http://private.example")
        }
    }

    private class StatusTransport(private val statusCode: Int) : HttpTransport {
        override fun execute(request: HttpRequest): HttpResponse = HttpResponse(statusCode, "{}")
    }

    private class ThrowingTransport : HttpTransport {
        override fun execute(request: HttpRequest): HttpResponse {
            error("连接失败 token=plain-token Authorization: Bearer plain-authorization http://private.example")
        }
    }
}
