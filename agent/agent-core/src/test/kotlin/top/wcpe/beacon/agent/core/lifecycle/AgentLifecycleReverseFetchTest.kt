package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.command.ReverseFetchExecutor
import top.wcpe.beacon.agent.core.config.ConfigApplier
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
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
 * AgentLifecycle 反向抓取命令触发单测（FR-39，见 ADR-0027）：
 * - SSE command-pending 事件 → 在 async 线程拉待办命令（命中 /agent/commands）；
 * - SSE READY → 也拉一次（兜断连期间排进来的命令）；
 * - 未注入执行器时 command-pending 不触发拉命令（向后兼容）。
 */
class AgentLifecycleReverseFetchTest {

    private val backend = FakeBeaconBackend()
    private val stream = FakeStreamTransport()
    private val adapter = ThreadPoolPlatformAdapter()
    private val store = EffectiveConfigStore()

    private fun identity() = AgentIdentity(
        namespace = "prod", serverId = "lobby-1", role = "bukkit", groupHint = "area1",
        address = "127.0.0.1:25565", version = "1.0", capacity = 100, weight = 1, metadata = emptyMap(),
    )

    private fun settings() = AgentSettings(
        endpoints = listOf("http://localhost:8848"),
        bootstrapToken = "tk",
        pollTimeoutMs = 50,
        requestTimeoutMs = 200,
        heartbeatFallbackMs = 100_000,
        backoff = BackoffSettings(initialMs = 50, maxMs = 50, multiplier = 1.0, jitterRatio = 0.0),
        snapshotEnabled = false,
        snapshotFileName = "snapshot.json",
        fileTree = FileTreeSettings(enabled = false, targetSubDir = "", appliedManifestFileName = "x.json"),
        override = OverrideSettings(commandWhitelist = emptySet(), backupDirName = "bk"),
    )

    /** 装配带反向抓取执行器的生命周期（执行器复用同一 backend，命令端点默认 204 无待办）。 */
    private fun newLifecycleWithExecutor(): AgentLifecycle {
        val codec = CannedJsonCodec()
        val apiClient = BeaconApiClient(backend, codec, settings(), stream)
        val applier = ConfigApplier(store, null, adapter)
        val executor = ReverseFetchExecutor(identity(), apiClient, adapter)
        return AgentLifecycle(
            identity(), settings(), adapter, apiClient, store, applier, null,
            reverseFetchExecutor = executor,
        )
    }

    /** 装配不带执行器的生命周期（断言未注入时不拉命令）。 */
    private fun newLifecycleWithoutExecutor(): AgentLifecycle {
        val codec = CannedJsonCodec()
        val apiClient = BeaconApiClient(backend, codec, settings(), stream)
        val applier = ConfigApplier(store, null, adapter)
        return AgentLifecycle(identity(), settings(), adapter, apiClient, store, applier, null)
    }

    @AfterTest
    fun tearDown() {
        adapter.shutdown()
    }

    @Test
    fun `command-pending 事件触发拉待办命令`() {
        val lifecycle = newLifecycleWithExecutor()
        lifecycle.bootstrapWithSnapshotThenConnect()
        waitUntil(2000) { stream.openCalls.get() >= 1 }
        // READY 也会触发一次拉命令；先记下基线，再单独验证 command-pending 增量。
        val before = backend.commandsCalls.get()

        stream.pushEvent(StreamEventTypes.COMMAND_PENDING)
        waitUntil(2000) { backend.commandsCalls.get() > before }
        assertTrue(backend.commandsCalls.get() > before, "command-pending 应触发一次拉待办命令")
    }

    @Test
    fun `READY 事件也触发拉待办命令`() {
        val lifecycle = newLifecycleWithExecutor()
        lifecycle.bootstrapWithSnapshotThenConnect()
        // FakeStreamTransport 在 onOpen 后不自动发 ready；显式推一条 READY 模拟对账完成。
        waitUntil(2000) { stream.openCalls.get() >= 1 }
        val before = backend.commandsCalls.get()

        stream.pushEvent(StreamEventTypes.READY)
        waitUntil(2000) { backend.commandsCalls.get() > before }
        assertTrue(backend.commandsCalls.get() > before, "READY 应兜底拉一次待办命令")
    }

    @Test
    fun `未注入执行器时 command-pending 不拉命令`() {
        val lifecycle = newLifecycleWithoutExecutor()
        lifecycle.bootstrapWithSnapshotThenConnect()
        waitUntil(2000) { stream.openCalls.get() >= 1 }
        val before = backend.commandsCalls.get()

        stream.pushEvent(StreamEventTypes.COMMAND_PENDING)
        // 给足时间确认确实没有拉命令（未注入执行器 → no-op）。
        Thread.sleep(200)
        assertTrue(backend.commandsCalls.get() == before, "未注入执行器不应拉命令，实际增量 ${backend.commandsCalls.get() - before}")
    }

    private fun waitUntil(timeoutMs: Long, cond: () -> Boolean) {
        val deadline = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(timeoutMs)
        while (System.nanoTime() < deadline) {
            if (cond()) return
            Thread.sleep(10)
        }
    }
}
