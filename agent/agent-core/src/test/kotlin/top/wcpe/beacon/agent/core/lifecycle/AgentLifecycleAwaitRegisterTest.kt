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
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * AgentLifecycle 「身份就绪有界等待」单测：覆盖首次注册成功放行闩的
 * 就绪前 false / 就绪后 true / 并发等待三点（供下游有界等待身份就绪用）。
 */
class AgentLifecycleAwaitRegisterTest {

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

    private fun settings() = AgentSettings(
        endpoints = listOf("http://localhost:8848"),
        bootstrapToken = "tk",
        pollTimeoutMs = 50,
        requestTimeoutMs = 200,
        heartbeatFallbackMs = 100_000,
        // 退避初始值给大，避免延迟重试在测试窗口内大量自调度干扰断言。
        backoff = BackoffSettings(initialMs = 60_000, maxMs = 60_000, multiplier = 1.0, jitterRatio = 0.0),
        snapshotEnabled = false,
        snapshotFileName = "snapshot.json",
        fileTree = FileTreeSettings(enabled = false, targetSubDir = "", appliedManifestFileName = "file-tree.applied.json"),
        override = OverrideSettings(commandWhitelist = emptySet(), backupDirName = "override-backup"),
    )

    private fun newLifecycle(): AgentLifecycle {
        val codec = CannedJsonCodec()
        val apiClient = BeaconApiClient(backend, codec, settings())
        val applier = ConfigApplier(store, null, adapter)
        return AgentLifecycle(identity(), settings(), adapter, apiClient, store, applier, null)
    }

    @AfterTest
    fun tearDown() {
        adapter.shutdown()
    }

    @Test
    fun `就绪前 awaitFirstRegister 超时返回 false`() {
        // 让注册一直挂着（不 release），首次注册始终未完成 → 闩未放行。
        val entered = CountDownLatch(1)
        val release = CountDownLatch(1)
        backend.registerEntered = entered
        backend.releaseRegister = release

        val lifecycle = newLifecycle()
        lifecycle.bootstrapWithSnapshotThenConnect()
        // 等首次注册确实进入在飞，确保此刻就是「注册中、尚未成功」的真空窗口。
        assertTrue(entered.await(2, TimeUnit.SECONDS), "首次注册应已进入在飞")

        // 闩未放行：有界等待应超时返回 false；timeoutMillis<=0 的只读查询也应为 false。
        assertFalse(lifecycle.awaitFirstRegister(50), "注册成功前应超时返回 false")
        assertFalse(lifecycle.awaitFirstRegister(0), "注册成功前只读查询应为 false")

        // 收尾：放行注册，避免线程池残留阻塞。
        release.countDown()
    }

    @Test
    fun `注册成功后 awaitFirstRegister 立即返回 true`() {
        val lifecycle = newLifecycle()
        // 注册不阻塞，立即成功。
        lifecycle.bootstrapWithSnapshotThenConnect()
        waitUntil(2000) { lifecycle.currentState() == AgentState.RUNNING }

        // 已就绪：只读查询与有界等待都立即返回 true。
        assertTrue(lifecycle.awaitFirstRegister(0), "注册成功后只读查询应为 true")
        assertTrue(lifecycle.awaitFirstRegister(1000), "注册成功后有界等待应为 true")
    }

    @Test
    fun `多线程并发等待在注册成功后全部返回 true`() {
        // 注册先挂住，开多个等待线程阻塞在闩上，再放行注册让其同时被唤醒。
        val entered = CountDownLatch(1)
        val release = CountDownLatch(1)
        backend.registerEntered = entered
        backend.releaseRegister = release

        val lifecycle = newLifecycle()
        lifecycle.bootstrapWithSnapshotThenConnect()
        assertTrue(entered.await(2, TimeUnit.SECONDS), "首次注册应已进入在飞")

        val trueCount = AtomicInteger(0)
        val starter = CountDownLatch(1)
        val waiters = (1..8).map {
            Thread {
                starter.await()
                // 给足够长的超时（远大于放行耗时），就绪后应被唤醒返回 true。
                if (lifecycle.awaitFirstRegister(3000)) trueCount.incrementAndGet()
            }.apply { start() }
        }
        starter.countDown()
        // 等待线程都已阻塞在闩上后再放行注册。
        Thread.sleep(100)
        release.countDown()

        waiters.forEach { it.join(4000) }
        assertEquals(8, trueCount.get(), "注册成功后所有并发等待者都应返回 true")
    }

    private fun waitUntil(timeoutMs: Long, cond: () -> Boolean) {
        val deadline = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(timeoutMs)
        while (System.nanoTime() < deadline) {
            if (cond()) return
            Thread.sleep(10)
        }
    }
}
