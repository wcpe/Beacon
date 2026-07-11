package top.wcpe.beacon.agent.core.scheduling

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.LocalDecisionReport
import java.io.File
import java.nio.file.Files
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNotNull
import kotlin.test.assertTrue

/**
 * [SchedulingRefresher] 单测（FR-148）：刷新拉候选写缓存 + 落盘、恢复后补报积压降级决策、
 * warn-once-on-transition、启动期从落盘快照恢复。用同步 runAsync 适配器使首帧刷新确定性发生。
 */
class SchedulingRefresherTest {
    private val tempDir: File = Files.createTempDirectory("sched-refresh").toFile()
    private val adapter = ManualSchedAdapter(tempDir)
    private val transport = SchedFakeTransport()
    private val cache = SchedulingCache(now = { 10_000L })
    private val reportQueue = LocalDecisionReportQueue()
    private val store = SchedulingSnapshotStore(File(tempDir, "candidates-snapshot.json"), RoundTripCodec())

    private fun newRefresher(): SchedulingRefresher =
        SchedulingRefresher(
            BeaconApiClient(transport, SchedCannedCodec(), schedSettings()),
            schedIdentity(),
            adapter,
            cache,
            store,
            reportQueue,
            now = { 10_000L },
        )

    @Test
    fun `启动即刷新一帧写缓存并落盘`() {
        newRefresher().start()
        // runAsync 同步 → 首帧刷新已发生。
        assertEquals(1, transport.candidatesCalls.get())
        assertEquals(2, cache.entriesInZone("z-a").size, "候选快照应写入缓存")
        assertNotNull(store.read(), "候选快照应落盘")
    }

    @Test
    fun `恢复后补报积压降级决策并清空队列`() {
        reportQueue.offer(
            LocalDecisionReport("local-1", 1L, "z-a", null, "降级", 1, emptyList(), "lobby-1", null),
        )
        newRefresher().start()
        assertEquals(1, transport.reportCalls.get(), "刷新成功应触发 report-local 补报")
        assertEquals(0, reportQueue.size(), "补报成功应清空队列")
    }

    @Test
    fun `补报失败退回队列下次重试`() {
        reportQueue.offer(
            LocalDecisionReport("local-1", 1L, "z-a", null, "降级", 1, emptyList(), "lobby-1", null),
        )
        transport.reportStatus = 500
        newRefresher().start()
        assertEquals(1, reportQueue.size(), "补报失败应退回队列（best-effort，下次刷新重试）")
    }

    @Test
    fun `刷新失败仅告警一次并转本地快照态`() {
        transport.down = true
        val refresher = newRefresher()
        refresher.start()
        // 首帧失败 → warn 一次；推进下一帧仍失败 → 不再 warn。
        adapter.drainOne()
        val failWarns = adapter.warns.count { it.contains("拉取调度候选失败") }
        assertEquals(1, failWarns, "连续失败只告警一次（warn-once）")
    }

    @Test
    fun `restoreSnapshot 从落盘快照恢复缓存`() {
        store.write(snapshotOf("z-b", listOf(candidateEntry("lobby-9", 55, "degraded")), generatedAtMs = 500L, savedAtMs = 500L))
        newRefresher().restoreSnapshot()
        assertEquals(1, cache.entriesInZone("z-b").size, "启动期应凭落盘快照恢复候选（重启后仍可降级）")
        assertTrue(cache.current()?.zones?.containsKey("z-b") == true)
    }
}
