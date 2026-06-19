package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.DiscoveryQuery
import top.wcpe.beacon.agent.api.TopologyListener
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.messaging.RosterDirectoryHolder
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.BackoffSettings
import top.wcpe.beacon.agent.core.settings.FileTreeSettings
import top.wcpe.beacon.agent.core.settings.OverrideSettings
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import top.wcpe.beacon.agent.core.transport.StreamListener
import top.wcpe.beacon.agent.core.transport.StreamRequest
import top.wcpe.beacon.agent.core.transport.StreamTransport
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicReference
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * DiscoveryView 单测（FR-29）：
 * - query 把 DiscoveryQuery.tags 透传到 discover 的 tags 入参（控制面拼 tag.<k>=<v>）；
 * - watch 注入流时注册到 TopologyWatchHub、句柄可注销；未注入流时回退为不可用 no-op 句柄。
 */
class DiscoveryViewTest {

    private fun settings() = AgentSettings(
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

    // 最小假 codec：encode 不用，decode 返回固定空 instances 树。
    private class FixedCodec : JsonCodec {
        override fun encode(value: Any?): String = "{}"
        override fun decode(json: String): Any? = mapOf("instances" to emptyList<Any?>())
    }

    // 记录最近一次请求 URL 的假 transport。
    private class CapturingTransport : HttpTransport {
        val lastUrl = AtomicReference<String?>(null)
        override fun execute(request: HttpRequest): HttpResponse {
            lastUrl.set(request.url)
            return HttpResponse(200, "{}")
        }
    }

    // 占位流传输：注入即视为"具备流能力"。
    private class NoopStreamTransport : StreamTransport {
        override fun open(request: StreamRequest, listener: StreamListener) { /* 测试不真正读流 */ }
    }

    @Test
    fun `query 透传 tags 到 discover 拼 tag 参数`() {
        val transport = CapturingTransport()
        val apiClient = BeaconApiClient(transport, FixedCodec(), settings(), NoopStreamTransport())
        val view = DiscoveryView(apiClient, TopologyWatchHub(), RosterDirectoryHolder())

        view.query(
            DiscoveryQuery.builder().namespace("prod").role("bukkit")
                .tag("region", "cn-east").build(),
        )
        val url = transport.lastUrl.get()!!
        assertTrue(url.contains("tag.region=cn-east"), "query 应把 tag 透传拼为 tag.region=cn-east，实际 $url")
    }

    @Test
    fun `watch 注入流时回调 hub 注销后不再回调`() {
        val hub = TopologyWatchHub()
        val apiClient = BeaconApiClient(CapturingTransport(), FixedCodec(), settings(), NoopStreamTransport())
        val view = DiscoveryView(apiClient, hub, RosterDirectoryHolder())

        val fired = AtomicInteger(0)
        val handle = view.watch(TopologyListener { fired.incrementAndGet() })
        // hub 触发 → 应回调。
        hub.fireTopologyChanged()
        assertEquals(1, fired.get(), "注入流时 watch 应注册到 hub 并在拓扑变更时回调")
        // 注销后再触发 → 不再回调。
        handle.remove()
        hub.fireTopologyChanged()
        assertEquals(1, fired.get(), "注销后不应再回调")
    }

    @Test
    fun `watch 未注入流时返回不可用 no-op 句柄`() {
        val hub = TopologyWatchHub()
        // 不注入 streamTransport → 无推送流能力（回退态）。
        val apiClient = BeaconApiClient(CapturingTransport(), FixedCodec(), settings())
        val view = DiscoveryView(apiClient, hub, RosterDirectoryHolder())

        val fired = AtomicInteger(0)
        val handle = view.watch(TopologyListener { fired.incrementAndGet() })
        // 即便 hub 被触发，回退态下也不应回调（监听器未注册到 hub）。
        hub.fireTopologyChanged()
        assertEquals(0, fired.get(), "回退态（未启用推送流）watch 不应回调")
        // remove 安全可重复调用。
        handle.remove()
        handle.remove()
    }
}
