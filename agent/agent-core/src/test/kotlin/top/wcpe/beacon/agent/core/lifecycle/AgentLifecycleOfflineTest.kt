package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.config.ConfigApplier
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.BackoffSettings
import top.wcpe.beacon.agent.core.settings.FileTreeSettings
import top.wcpe.beacon.agent.core.settings.OverrideSettings
import top.wcpe.beacon.agent.core.testutil.CannedJsonCodec
import top.wcpe.beacon.agent.core.testutil.FakeBeaconBackend
import top.wcpe.beacon.agent.core.testutil.ThreadPoolPlatformAdapter
import java.util.concurrent.TimeUnit
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * AgentLifecycle 主动下线态（FR-49）单测：注册被 403 拒 → 进 OFFLINE、停止猛重连；
 * 取消下线后降频探测可恢复 RUNNING；OFFLINE 与 DEGRADED（控制面不可用）严格区分。
 */
class AgentLifecycleOfflineTest {

    private val backend = FakeBeaconBackend()
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

    // offlineProbeMs 给小值（80ms）以便测试内能观察到「取消下线后探测恢复」，又远大于单次注册耗时。
    private fun settings(offlineProbeMs: Long = 80) = AgentSettings(
        endpoints = listOf("http://localhost:8848"),
        bootstrapToken = "tk",
        pollTimeoutMs = 50,
        requestTimeoutMs = 200,
        heartbeatFallbackMs = 100_000,
        // 退避初始值给大，确保「停止猛重连」断言不被退避重试链干扰。
        backoff = BackoffSettings(initialMs = 60_000, maxMs = 60_000, multiplier = 1.0, jitterRatio = 0.0),
        snapshotEnabled = false,
        snapshotFileName = "snapshot.json",
        fileTree = FileTreeSettings(enabled = false, targetSubDir = "", appliedManifestFileName = "file-tree.applied.json"),
        override = OverrideSettings(commandWhitelist = emptySet(), backupDirName = "override-backup"),
        offlineProbeIntervalMs = offlineProbeMs,
    )

    private fun newLifecycle(offlineProbeMs: Long = 80): AgentLifecycle {
        val codec = CannedJsonCodec()
        val apiClient = BeaconApiClient(backend, codec, settings(offlineProbeMs))
        val applier = ConfigApplier(store, null, adapter)
        return AgentLifecycle(identity(), settings(offlineProbeMs), adapter, apiClient, store, applier, null)
    }

    @AfterTest
    fun tearDown() {
        adapter.shutdown()
    }

    @Test
    fun `注册被 403 拒进入 OFFLINE 且不猛重连`() {
        // 探测间隔给足够大（与退避同量级），保证观察窗口内不会因探测再发注册。
        backend.registerStatus = 403
        val lifecycle = newLifecycle(offlineProbeMs = 60_000)
        lifecycle.bootstrapWithSnapshotThenConnect()

        // 进入 OFFLINE 态（被主动下线，区别于 DEGRADED）。
        waitUntil(2000) { lifecycle.currentState() == AgentState.OFFLINE }
        assertEquals(AgentState.OFFLINE, lifecycle.currentState(), "被 403 拒应进入 OFFLINE 态")

        // 记下当前注册次数，等一段明显超过任意常规重试节奏的时间，注册次数不得继续增长（停止猛重连）。
        val callsAfterReject = backend.registerCalls.get()
        Thread.sleep(400)
        assertEquals(
            callsAfterReject,
            backend.registerCalls.get(),
            "OFFLINE 态不得猛重连：观察窗口内注册次数不应增长",
        )
        // fail-static：被拒不阻断、不进 RUNNING、状态仍 OFFLINE。
        assertEquals(AgentState.OFFLINE, lifecycle.currentState())
    }

    @Test
    fun `取消下线后降频探测可恢复 RUNNING`() {
        backend.registerStatus = 403
        val lifecycle = newLifecycle(offlineProbeMs = 80)
        lifecycle.bootstrapWithSnapshotThenConnect()
        waitUntil(2000) { lifecycle.currentState() == AgentState.OFFLINE }

        // 模拟控制面取消下线：后续注册放行。降频探测下一跳即应注册成功、回 RUNNING。
        backend.registerStatus = 200
        waitUntil(3000) { lifecycle.currentState() == AgentState.RUNNING }
        assertEquals(AgentState.RUNNING, lifecycle.currentState(), "取消下线后降频探测应恢复 RUNNING")
    }

    @Test
    fun `OFFLINE 区别于连接失败的 DEGRADED`() {
        // 注册连接级失败（404 之外的非预期：这里用端点全 404 模拟，register 命中 else 分支 Failed）→ DEGRADED，绝不 OFFLINE。
        backend.registerStatus = 500
        val lifecycle = newLifecycle(offlineProbeMs = 60_000)
        lifecycle.bootstrapWithSnapshotThenConnect()
        waitUntil(2000) { lifecycle.currentState() == AgentState.DEGRADED }
        assertEquals(AgentState.DEGRADED, lifecycle.currentState(), "连接级失败应 DEGRADED，不得当作主动下线")
        assertTrue(lifecycle.currentState() != AgentState.OFFLINE, "连接失败绝不进 OFFLINE")
    }

    private fun waitUntil(timeoutMs: Long, cond: () -> Boolean) {
        val deadline = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(timeoutMs)
        while (System.nanoTime() < deadline) {
            if (cond()) return
            Thread.sleep(10)
        }
    }
}
