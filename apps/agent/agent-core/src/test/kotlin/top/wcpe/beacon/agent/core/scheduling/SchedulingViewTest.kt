package top.wcpe.beacon.agent.core.scheduling

import top.wcpe.beacon.agent.api.DecisionSource
import top.wcpe.beacon.agent.api.HealthLevel
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.SelfHealth
import java.io.File
import java.nio.file.Files
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * [SchedulingView] 单测（FR-148）：控制面在线决策、控制面不可达 fail-static 本地降级（不抛、不阻塞、入补报队列）、
 * 只读缓存查询、selfHealth。用同步 runAsync 适配器使 acquireCandidate 的 future 确定性完成。
 */
class SchedulingViewTest {
    private val tempDir: File = Files.createTempDirectory("sched-view").toFile()
    private val adapter = ManualSchedAdapter(tempDir)
    private val transport = SchedFakeTransport()
    private val cache = SchedulingCache(now = { 10_000L })
    private val reportQueue = LocalDecisionReportQueue()
    private val selfHealthHolder = SelfHealthHolder(now = { 10_000L })

    private fun newView(): SchedulingView {
        val apiClient = BeaconApiClient(transport, SchedCannedCodec(), schedSettings())
        return SchedulingView(apiClient, schedIdentity(), adapter, cache, reportQueue, selfHealthHolder, now = { 10_000L })
    }

    private fun populate() {
        cache.set(
            snapshotOf(
                "z-a",
                listOf(candidateEntry("lobby-1", 90, "healthy", true, 3, 100), candidateEntry("lobby-2", 70, "degraded", true, 8, 100)),
                savedAtMs = 10_000L,
            ),
            live = true,
        )
    }

    @Test
    fun `控制面在线决策返回 CONTROL_PLANE 并补全 chosen 视图`() {
        populate()
        val result = newView().acquireCandidate("z-a").get()
        assertEquals(DecisionSource.CONTROL_PLANE, result.source())
        assertEquals("srv-trace-1", result.traceId(), "应用服务端 traceId")
        assertNull(result.failReason())
        val chosen = result.chosen()
        assertEquals("lobby-1", chosen?.serverId())
        assertEquals(90, chosen?.score())
        // level/online/max 从本地快照补全。
        assertEquals(HealthLevel.HEALTHY, chosen?.level())
        assertEquals(3, chosen?.onlineCount())
        assertEquals(100, chosen?.maxOnline())
        assertEquals(0, reportQueue.size(), "在线决策不入补报队列")
    }

    @Test
    fun `控制面不可达降级本地 highest_score 且入补报队列`() {
        populate()
        transport.down = true
        val result = newView().acquireCandidate("z-a", "lobby-transfer").get()
        assertEquals(DecisionSource.LOCAL_FALLBACK, result.source())
        assertEquals("lobby-1", result.chosen()?.serverId(), "本地 highest_score 应选分最高者")
        assertEquals(90, result.chosen()?.score())
        assertNull(result.failReason())
        assertEquals(1, reportQueue.size(), "降级决策应入补报队列")
        val queued = reportQueue.drain().single()
        assertEquals("lobby-transfer", queued.purpose)
        assertEquals("lobby-1", queued.chosenServerId)
        assertEquals(2, queued.candidateCount)
        assertEquals(result.traceId(), queued.localTraceId, "补报 localTraceId 即本次本地 traceId")
    }

    @Test
    fun `控制面不可达且快照为空降级为 no_candidate 不抛异常`() {
        transport.down = true
        // 未 populate：快照空。
        val future = newView().acquireCandidate("z-a")
        val result = future.get()
        assertFalse(future.isCompletedExceptionally, "fail-static：future 绝不异常完成")
        assertEquals(DecisionSource.LOCAL_FALLBACK, result.source())
        assertNull(result.chosen())
        assertEquals("no_candidate", result.failReason())
    }

    @Test
    fun `zone 不存在如实回控制面失败不降级`() {
        populate()
        transport.decideStatus = 404
        val result = newView().acquireCandidate("z-x").get()
        assertEquals(DecisionSource.CONTROL_PLANE, result.source())
        assertEquals("zone_not_found", result.failReason())
        assertNull(result.chosen())
        assertEquals(0, reportQueue.size(), "控制面权威失败不入补报队列")
    }

    @Test
    fun `candidatesInZone 与 healthOf 读缓存快照`() {
        populate()
        val view = newView()
        val cands = view.candidatesInZone("z-a")
        assertEquals(listOf("lobby-1", "lobby-2"), cands.map { it.serverId() })
        assertEquals(HealthLevel.DEGRADED, cands.first { it.serverId() == "lobby-2" }.level())
        assertTrue(view.candidatesInZone("z-none").isEmpty())

        val health = view.healthOf("lobby-2")
        assertEquals(70, health?.score())
        assertEquals(HealthLevel.DEGRADED, health?.level())
        assertNull(view.healthOf("nope"))
    }

    @Test
    fun `selfHealth 读自身健康持有者`() {
        val view = newView()
        assertNull(view.selfHealth(), "从未上报时为 null")
        selfHealthHolder.set(SelfHealth(score = 82, level = "healthy", schedulable = true, reasons = emptyList()))
        val self = view.selfHealth()
        assertEquals("lobby-1", self?.serverId())
        assertEquals(82, self?.score())
        assertEquals(HealthLevel.HEALTHY, self?.level())
        assertEquals(10_000L, self?.sampledAtMs())
    }
}
