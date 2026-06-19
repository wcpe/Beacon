package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.config.ConfigApplier
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.config.EffectiveResult
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.BackoffSettings
import top.wcpe.beacon.agent.core.settings.FileTreeSettings
import top.wcpe.beacon.agent.core.settings.OverrideSettings
import top.wcpe.beacon.agent.core.stream.StreamEventTypes
import top.wcpe.beacon.agent.core.testutil.CannedJsonCodec
import top.wcpe.beacon.agent.core.testutil.FakeBeaconBackend
import top.wcpe.beacon.agent.core.testutil.FakeStreamTransport
import top.wcpe.beacon.agent.core.testutil.ThreadPoolPlatformAdapter
import java.util.concurrent.TimeUnit
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertTrue

/**
 * AgentLifecycle 单条 SSE 推送流（FR-24）单测：
 * - 注入 streamTransport 后以 SSE 取代三条长轮询（不再轮询 config/effective 续杯）；
 * - 连接即对账：open 的 URL 携带各通道当前 md5；
 * - config-changed 事件触发取配置-应用；
 * - 流断后退避重连（再次 open）。
 */
class AgentLifecycleStreamTest {

    private val backend = FakeBeaconBackend()
    private val stream = FakeStreamTransport()
    private val adapter = ThreadPoolPlatformAdapter()
    private val store = EffectiveConfigStore()

    private fun identity() = AgentIdentity(
        namespace = "prod",
        serverId = "lobby-1",
        role = "bukkit",
        groupHint = "area1",
        address = "127.0.0.1:25565",
        version = "1.0",
        capacity = 100,
        weight = 1,
        metadata = emptyMap(),
    )

    private fun settings() = AgentSettings(
        endpoints = listOf("http://localhost:8848"),
        bootstrapToken = "tk",
        pollTimeoutMs = 50,
        requestTimeoutMs = 200,
        heartbeatFallbackMs = 100_000,
        // 退避初始值给小，便于断言流断后快速重连。
        backoff = BackoffSettings(initialMs = 50, maxMs = 50, multiplier = 1.0, jitterRatio = 0.0),
        snapshotEnabled = false,
        snapshotFileName = "snapshot.json",
        fileTree = FileTreeSettings(enabled = false, targetSubDir = "", appliedManifestFileName = "file-tree.applied.json"),
        override = OverrideSettings(commandWhitelist = emptySet(), backupDirName = "override-backup"),
    )

    private fun newLifecycle(onTopologyChanged: (() -> Unit)? = null): AgentLifecycle {
        val codec = CannedJsonCodec()
        // 注入 streamTransport → 启用 SSE 流，取代三条长轮询。
        val apiClient = BeaconApiClient(backend, codec, settings(), stream)
        val applier = ConfigApplier(store, null, adapter)
        return AgentLifecycle(
            identity(), settings(), adapter, apiClient, store, applier, null,
            topologyListener = onTopologyChanged,
        )
    }

    @AfterTest
    fun tearDown() {
        adapter.shutdown()
    }

    @Test
    fun `注入流传输后启用 SSE 流而非长轮询`() {
        val lifecycle = newLifecycle()
        lifecycle.bootstrapWithSnapshotThenConnect()
        // 注册成功后应建立 SSE 流（open 至少一次）。
        waitUntil(2000) { stream.openCalls.get() >= 1 }
        assertTrue(stream.openCalls.get() >= 1, "应启用 SSE 流（open 被调用）")
        // 不应走配置长轮询续杯（poll 端点零调用）。
        Thread.sleep(150)
        assertTrue(backend.pollCalls.get() == 0, "SSE 模式下不应再轮询 config/effective，实际 ${backend.pollCalls.get()} 次")
    }

    @Test
    fun `连接即对账 open URL 携带配置 md5`() {
        // 让本地先有一版有效配置（store 有 md5），SSE open 时应把它上报到 URL。
        store.replace(
            EffectiveResult(
                namespace = "prod", serverId = "lobby-1", group = "area1", zone = null,
                md5 = "local-md5", items = emptyList(),
            ),
        )
        val lifecycle = newLifecycle()
        lifecycle.bootstrapWithSnapshotThenConnect()
        waitUntil(2000) { stream.lastRequest.get() != null }
        val url = stream.lastRequest.get()!!.url
        assertTrue(url.contains("configMd5=local-md5"), "open URL 应携带本地配置 md5 供对账，实际 $url")
    }

    @Test
    fun `config-changed 事件触发取配置并 apply`() {
        backend.pollStatus = 200 // 取配置返回 200 + canned 内容
        val lifecycle = newLifecycle()
        lifecycle.bootstrapWithSnapshotThenConnect()
        waitUntil(2000) { stream.openCalls.get() >= 1 }

        // 直播阶段推一条 config-changed → 应触发 pollEffective 并 apply（store 落到 canned md5）。
        stream.pushEvent(StreamEventTypes.CONFIG_CHANGED, "{\"md5\":\"md5-v1\"}")
        waitUntil(2000) { store.currentMd5() == "md5-v1" }
        assertTrue(store.currentMd5() == "md5-v1", "config-changed 应触发取配置并 apply 到新 md5")
        assertTrue(backend.pollCalls.get() >= 1, "config-changed 应触发一次 config/effective 取数据")
    }

    @Test
    fun `topology-changed 事件触发拓扑监听器回调`() {
        val fired = java.util.concurrent.atomic.AtomicInteger(0)
        val lifecycle = newLifecycle(onTopologyChanged = { fired.incrementAndGet() })
        lifecycle.bootstrapWithSnapshotThenConnect()
        waitUntil(2000) { stream.openCalls.get() >= 1 }

        // 直播阶段推一条 topology-changed → 应回调拓扑监听器（业务侧据此重查发现端点）。
        stream.pushEvent(StreamEventTypes.TOPOLOGY_CHANGED, "{\"md5\":\"topo-v1\"}")
        waitUntil(2000) { fired.get() >= 1 }
        assertTrue(fired.get() >= 1, "topology-changed 应触发拓扑监听器回调")
        // 拓扑事件不应触发取配置（控制面不在事件里搬实例数据）。
        assertTrue(backend.pollCalls.get() == 0, "拓扑事件不应触发 config/effective 取数据")
    }

    @Test
    fun `连接即对账 open URL 携带空拓扑摘要`() {
        val lifecycle = newLifecycle()
        lifecycle.bootstrapWithSnapshotThenConnect()
        waitUntil(2000) { stream.lastRequest.get() != null }
        val url = stream.lastRequest.get()!!.url
        // agent 不本地维护拓扑，首连上报空拓扑摘要让控制面补一次 topology-changed。
        assertTrue(url.contains("topologyMd5="), "open URL 应携带 topologyMd5 参数，实际 $url")
    }

    @Test
    fun `流断后退避重连再次建立流`() {
        val lifecycle = newLifecycle()
        lifecycle.bootstrapWithSnapshotThenConnect()
        waitUntil(2000) { stream.openCalls.get() >= 1 }
        val before = stream.openCalls.get()

        // 模拟服务端关闭流 → 生命周期应退避后重连（再次 open）。
        stream.closeStream(null)
        waitUntil(2000) { stream.openCalls.get() > before }
        assertTrue(stream.openCalls.get() > before, "流断后应退避重连再次 open，实际仍为 $before")
    }

    private fun waitUntil(timeoutMs: Long, cond: () -> Boolean) {
        val deadline = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(timeoutMs)
        while (System.nanoTime() < deadline) {
            if (cond()) return
            Thread.sleep(10)
        }
    }
}
