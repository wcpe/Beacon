package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.config.ConfigApplier
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.BackoffSettings
import top.wcpe.beacon.agent.core.settings.FileTreeSettings
import top.wcpe.beacon.agent.core.testutil.CannedJsonCodec
import top.wcpe.beacon.agent.core.testutil.FakeBeaconBackend
import top.wcpe.beacon.agent.core.testutil.ThreadPoolPlatformAdapter
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * AgentLifecycle 本地控制入口（FR-17）与注册单飞不变量的并发单测。
 *
 * 不变量：任意时刻只有一条 register→loops 在飞——并发 reconnect / reconnect 与 poll 并发
 * 都不得出现两条 register 同时进行（maxConcurrentRegister == 1）。
 */
class AgentLifecycleControlTest {

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
    fun `连续 reconnect 不出现并行注册`() {
        // 让注册阻塞以放大并发窗口：注册一进入即开 entered、再挂在 release 上。
        val entered = CountDownLatch(1)
        val release = CountDownLatch(1)
        backend.registerEntered = entered
        backend.releaseRegister = release

        val lifecycle = newLifecycle()
        // bootstrap 置 running=true 并发起首次注册（将阻塞在 release 上）。
        lifecycle.bootstrapWithSnapshotThenConnect()
        // 等首次注册确实进入「在飞」。
        assertTrue(entered.await(2, TimeUnit.SECONDS), "首次注册应已进入在飞")

        // 在首次注册仍在飞期间，并发猛打 reconnect——单飞门应让它们全部 no-op 或排队，绝不并行进 register。
        val starter = CountDownLatch(1)
        val threads = (1..16).map {
            Thread {
                starter.await()
                repeat(3) { lifecycle.reconnectNow() }
            }.apply { start() }
        }
        starter.countDown()
        threads.forEach { it.join(2000) }

        // 给被调度的 beginRegister 一点时间真正跑起来（若无单飞，多个会同时进入 handleRegister）。
        Thread.sleep(200)
        // 此刻仍持有 release：在飞 register 不超过 1。
        assertEquals(1, backend.maxConcurrentRegister.get(), "任意时刻只能有一条 register 在飞")

        // 释放，等收敛。
        release.countDown()
        waitUntil(3000) { lifecycle.currentState() == AgentState.RUNNING }
        assertEquals(1, backend.maxConcurrentRegister.get(), "收敛后峰值仍为 1")
    }

    @Test
    fun `reconnect 与正常 poll 并发时注册单飞`() {
        val lifecycle = newLifecycle()
        // 正常接入（注册不阻塞，立即返回）。
        lifecycle.bootstrapWithSnapshotThenConnect()
        waitUntil(2000) { lifecycle.currentState() == AgentState.RUNNING }

        // 在长轮询持续续杯的同时，并发打多次 reconnect。
        val starter = CountDownLatch(1)
        val threads = (1..12).map {
            Thread {
                starter.await()
                repeat(3) { lifecycle.reconnectNow() }
            }.apply { start() }
        }
        starter.countDown()
        threads.forEach { it.join(3000) }

        waitUntil(2000) { lifecycle.currentState() == AgentState.RUNNING }
        assertTrue(backend.maxConcurrentRegister.get() <= 1, "reconnect 与 poll 并发下仍须单飞")
    }

    @Test
    fun `forcePollNow 以空 md5 强制重拉并 apply`() {
        backend.pollStatus = 200
        val lifecycle = newLifecycle()
        lifecycle.bootstrapWithSnapshotThenConnect()
        // 等有效配置经主循环 apply 落地，主循环此后以 md5-v1 续杯（非空 md5）。
        waitUntil(2000) { store.currentMd5() == "md5-v1" }

        // 快照「空 md5 拉取」计数：主循环此后只用 md5-v1，不再贡献空 md5。
        val emptyBefore = backend.emptyMd5PollCalls.get()
        lifecycle.forcePollNow()
        // forcePollNow 必须额外发一发空 md5（旁路 304）。
        waitUntil(2000) { backend.emptyMd5PollCalls.get() > emptyBefore }
        assertTrue(backend.emptyMd5PollCalls.get() > emptyBefore, "forcePollNow 必须以空 md5 旁路 304")
        // 有效配置仍为 md5-v1（apply 幂等守卫，内容未变不重复广播）。
        assertEquals("md5-v1", store.currentMd5())
    }

    @Test
    fun `reconnectNow 不清空已有 store 快照`() {
        backend.pollStatus = 200
        val lifecycle = newLifecycle()
        lifecycle.bootstrapWithSnapshotThenConnect()
        waitUntil(2000) { store.currentMd5() == "md5-v1" }

        lifecycle.reconnectNow()
        // 重连过程中 store 不得被清空（fail-static）。
        assertEquals("md5-v1", store.currentMd5(), "reconnect 不得清空已点亮的有效配置")
    }

    @Test
    fun `snapshot 反映当前状态`() {
        val lifecycle = newLifecycle()
        lifecycle.bootstrapWithSnapshotThenConnect()
        waitUntil(2000) { lifecycle.currentState() == AgentState.RUNNING }

        val snap = lifecycle.snapshot()
        assertEquals(AgentState.RUNNING, snap.state)
        assertTrue(snap.connected)
        assertEquals("http://localhost:8848", snap.endpoint)
        assertEquals(10, snap.heartbeatIntervalSec)
    }

    private fun waitUntil(timeoutMs: Long, cond: () -> Boolean) {
        val deadline = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(timeoutMs)
        while (System.nanoTime() < deadline) {
            if (cond()) return
            Thread.sleep(10)
        }
    }
}
