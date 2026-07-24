package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.metrics.RuntimeMetrics
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.BackoffSettings
import top.wcpe.beacon.agent.core.settings.FileTreeSettings
import top.wcpe.beacon.agent.core.settings.OverrideSettings
import top.wcpe.beacon.agent.core.testutil.CannedJsonCodec
import top.wcpe.beacon.agent.core.testutil.FakeBeaconBackend
import java.io.File
import java.util.concurrent.atomic.AtomicInteger
import kotlin.test.Test
import kotlin.test.assertEquals

/**
 * [MetricsSamplingCoordinator] start 幂等性单测（FR-144 §3.1：sample_count 须为 1~5）。
 *
 * 复现缺陷：每次注册成功都会调 start()，原实现非幂等——重复 start 无条件补打立即帧并递增代，
 * 旧代循环要到下一 tick 才退出，退出前仍产样本 → 启动爬坡期（心跳 404 / 断连恢复等多次重注册）
 * 同一 5s 桶挤入 >5 个 1s 样本。
 *
 * 用手动调度适配器（立即任务同步执行、延迟任务入队由测试显式推进）确定性断言帧数，不依赖真实时钟。
 */
class MetricsSamplingCoordinatorTest {
    /**
     * 手动调度适配器：runAsync 同步执行（承载 start 的立即帧），runAsyncDelayed 只入队不执行，
     * 由测试显式排空推进——使「重复 start 是否补帧 / 旧循环是否延续」可确定性断言。
     */
    private class ManualPlatformAdapter : PlatformAdapter {
        /** 延迟任务队列（FIFO，测试显式推进）。 */
        val delayed = ArrayDeque<() -> Unit>()

        override fun runAsync(task: () -> Unit) {
            task()
        }

        override fun runAsyncDelayed(
            delayMs: Long,
            task: () -> Unit,
        ) {
            delayed.addLast(task)
        }

        override fun runSync(task: () -> Unit) {
            task()
        }

        override fun dataFolder(): File = File(System.getProperty("java.io.tmpdir"))

        override fun publishConfigChanged(
            changed: Set<String>,
            newMd5: String,
        ) {}

        override fun info(msg: String) {}

        override fun warn(msg: String) {}

        override fun error(
            msg: String,
            t: Throwable?,
        ) {}

        /** 推进一个延迟任务。 */
        fun drainOne() {
            delayed.removeFirst().invoke()
        }

        /** 排空全部延迟任务（stop 后 tick 不再续杯，必然终止）。 */
        fun drainAll() {
            while (delayed.isNotEmpty()) delayed.removeFirst().invoke()
        }
    }

    private val adapter = ManualPlatformAdapter()

    /** 采样帧计数：samplingTick 每帧调一次 runtimeProvider，计数即帧数。 */
    private val frames = AtomicInteger(0)

    private fun identity() =
        AgentIdentity(
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

    private fun settings() =
        AgentSettings(
            endpoints = listOf("http://localhost:8848"),
            bootstrapToken = "tk",
            pollTimeoutMs = 50,
            requestTimeoutMs = 200,
            heartbeatFallbackMs = 100_000,
            backoff = BackoffSettings(initialMs = 60_000, maxMs = 60_000, multiplier = 1.0, jitterRatio = 0.0),
            snapshotEnabled = false,
            snapshotFileName = "snapshot.json",
            fileTree = FileTreeSettings(enabled = false, targetSubDir = "", appliedManifestFileName = "file-tree.applied.json"),
            override = OverrideSettings(commandWhitelist = emptySet(), backupDirName = "override-backup"),
        )

    private fun newCoordinator(): MetricsSamplingCoordinator {
        val coordinator =
            MetricsSamplingCoordinator(
                adapter = adapter,
                apiClient = BeaconApiClient(FakeBeaconBackend(), CannedJsonCodec(), settings()),
                identity = identity(),
                runtimeProvider = {
                    frames.incrementAndGet()
                    RuntimeMetrics(playerCount = 7, tps = 19.5, memUsed = 256L * 1024 * 1024, memMax = 512L * 1024 * 1024, cpuLoad = 0.4)
                },
                proxyProvider = { null },
            )
        // 周期给大：延迟任务只入队，帧数完全由测试显式推进决定。
        coordinator.configure(sampleIntervalMs = 60_000, batchReportIntervalMs = 60_000, bucketMs = 5_000)
        return coordinator
    }

    @Test
    fun `重复 start 不补打立即帧`() {
        val coordinator = newCoordinator()
        coordinator.start()
        assertEquals(1, frames.get(), "首次 start 应补打一帧立即样本")

        // 模拟重注册（心跳 404 / 断连恢复触发再次注册成功）再次 start：
        // 不应再补立即帧，否则叠加旧代循环样本会把 >5 帧挤进同一 5s 桶。
        coordinator.start()
        assertEquals(1, frames.get(), "重复 start（重注册）不应再补打立即帧")
    }

    @Test
    fun `重复 start 不中断原采样循环`() {
        val coordinator = newCoordinator()
        coordinator.start()
        coordinator.start()

        // 队首是首次 start 立即帧续杯的采样 tick：重复 start 不应使其因代失配退出。
        adapter.drainOne()
        assertEquals(2, frames.get(), "重复 start 后原采样循环应继续产帧")
    }

    @Test
    fun `stop 后重新 start 应重启采样`() {
        val coordinator = newCoordinator()
        coordinator.start()
        assertEquals(1, frames.get())

        coordinator.stop()
        // stop 前遗留的旧 tick：active 已为 false，应直接退出、不再采样。
        adapter.drainAll()
        assertEquals(1, frames.get(), "stop 后旧 tick 不应再采样")

        coordinator.start()
        assertEquals(2, frames.get(), "stop 后重新 start 应补打立即帧重启采样")
    }
}
