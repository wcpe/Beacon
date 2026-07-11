package top.wcpe.beacon.agent.core.sampling

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * 采样环形缓冲 [MetricSampleBuffer] 单测（FR-144 §4.1 / §4.2，高风险区穷举）。
 *
 * 覆盖：写满覆盖最旧 + droppedCount、已闭合桶快照保留当前桶、上报确认按 seq 精确移除、
 * 失败保留重试、上报在途覆盖丢弃不误删、droppedSinceLast 随成功批扣减且残余结转。
 */
class MetricSampleBufferTest {
    private fun backend(tsMs: Long): MetricSample =
        MetricSample(
            tsMs = tsMs,
            cpuPct = 10.0,
            memUsedMb = 100.0,
            memMaxMb = 200,
            reportRttMs = -1,
            payload = BackendSample(tps = 20.0, onlineCount = 1, maxOnline = 100),
        )

    @Test
    fun `写满覆盖最旧并累加丢弃计数`() {
        val buf = MetricSampleBuffer(capacity = 3)
        // 加 5 条，容量 3 → 覆盖最旧 2 条。
        for (i in 1..5) buf.add(backend(i * 1000L))
        assertEquals(3, buf.size(), "容量满后应稳定在容量大小")
        assertEquals(2L, buf.droppedCount(), "应累加被覆盖丢弃的样本数")
    }

    @Test
    fun `已闭合桶快照保留当前采集桶`() {
        val buf = MetricSampleBuffer(capacity = 100)
        // 桶0：ts 1000/2000；桶5000：ts 6000。
        buf.add(backend(1000L))
        buf.add(backend(2000L))
        buf.add(backend(6000L))
        // now=7000：桶0（0+5000<=7000）已闭合；桶5000（5000+5000=10000>7000）未闭合，应保留。
        val snap = buf.peekClosed(nowMs = 7000L)
        assertEquals(2, snap.samples.size, "仅返回已闭合桶（桶0）的样本")
        assertTrue(snap.samples.all { it.tsMs < 5000L }, "当前采集桶（桶5000）不应上报")
    }

    @Test
    fun `无已闭合桶时快照为空`() {
        val buf = MetricSampleBuffer(capacity = 100)
        buf.add(backend(6000L)) // 桶5000
        val snap = buf.peekClosed(nowMs = 7000L) // 桶5000 未闭合
        assertTrue(snap.samples.isEmpty(), "当前桶未闭合应无可上报样本")
        assertEquals(-1L, snap.lastSeq, "空快照 lastSeq 为 -1")
    }

    @Test
    fun `上报确认按 seq 移除已上报前缀失败则保留`() {
        val buf = MetricSampleBuffer(capacity = 100)
        buf.add(backend(1000L)) // 桶0
        buf.add(backend(2000L)) // 桶0
        val snap = buf.peekClosed(nowMs = 10_000L)
        assertEquals(2, snap.samples.size)
        // 未 ack（模拟上报失败）：样本仍在缓冲，可再次上报。
        assertEquals(2, buf.size(), "上报失败不移除样本，留待重试")
        // ack 成功：移除已上报前缀。
        buf.ack(snap.lastSeq, snap.droppedSinceLast)
        assertEquals(0, buf.size(), "上报成功后应移除已上报样本")
    }

    @Test
    fun `上报在途覆盖丢弃不误删更新样本`() {
        val buf = MetricSampleBuffer(capacity = 3)
        buf.add(backend(1000L)) // seq0 桶0
        buf.add(backend(2000L)) // seq1 桶0
        val snap = buf.peekClosed(nowMs = 10_000L) // 取 seq0/seq1
        // 上报在途：新样本涌入使容量满，覆盖最旧（seq0/seq1 被挤掉）。
        buf.add(backend(11_000L)) // seq2
        buf.add(backend(12_000L)) // seq3
        buf.add(backend(13_000L)) // seq4，容量3 → 覆盖 seq0
        buf.add(backend(14_000L)) // seq5，覆盖 seq1
        // 此刻缓冲为 seq3/seq4/seq5。ack(lastSeq=1)：seq<=1 均已被覆盖不在缓冲 → 不误删 seq3+。
        buf.ack(snap.lastSeq, snap.droppedSinceLast)
        assertEquals(3, buf.size(), "ack 只移除仍在缓冲且 seq<=lastSeq 者，不误删更新样本")
    }

    @Test
    fun `成功批扣减丢弃计数在途新增丢弃结转下批`() {
        val buf = MetricSampleBuffer(capacity = 2)
        buf.add(backend(1000L)) // seq0
        buf.add(backend(2000L)) // seq1
        buf.add(backend(3000L)) // seq2 → 覆盖 seq0，dropped=1
        val snap = buf.peekClosed(nowMs = 10_000L)
        assertEquals(1L, snap.droppedSinceLast, "快照应带当前丢弃数")
        // 上报在途再丢弃一条。
        buf.add(backend(4000L)) // 覆盖 seq1，dropped=2
        // ack 扣减随本批上报的 1 条，残余 1 条结转。
        buf.ack(snap.lastSeq, snap.droppedSinceLast)
        assertEquals(1L, buf.droppedCount(), "在途新增丢弃应结转下批")
    }

    @Test
    fun `单批桶数上限截断超出桶下批继续`() {
        val buf = MetricSampleBuffer(capacity = 100)
        // 3 个已闭合桶：桶0/桶5000/桶10000（各一条）。
        buf.add(backend(1000L))
        buf.add(backend(6000L))
        buf.add(backend(11_000L))
        val snap = buf.peekClosed(nowMs = 60_000L, maxBuckets = 2)
        assertEquals(2, snap.samples.size, "单批最多取 maxBuckets 个桶的样本")
    }
}
