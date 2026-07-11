package top.wcpe.beacon.agent.core.sampling

/**
 * 采样样本有界环形缓冲（FR-144 §4.1 / §4.2）。线程安全：采样线程 [add] 与上报线程 [peekClosed]/[ack] 并发。
 *
 * 语义：
 * - 容量满、样本未及上报 → 覆盖**最旧**样本并 `droppedSinceLast++`（上次成功上报以来被覆盖丢弃数，§4.2）。
 * - 上报取「已闭合桶」（`bucketStart + bucketMs <= nowMs`）的样本，保留仍在采集的当前桶不上报——
 *   保证每个 5s 桶只被完整聚合上报一次，不会跨批切分致 `(serverId, bucketStart)` 去重冲突丢数据。
 * - 单批最多 [maxBuckets] 个桶（协议 §5.1 单批 ≤120），超出的更旧桶下一 tick 继续（断连补报即此机制的自然结果）。
 * - 上报成功才 [ack] 移除；失败不移除，样本留缓冲下一 tick 重试（§4.2「缓冲即背压」）。
 *
 * 用单调 seq 标记样本：[peekClosed] 返回快照的末样本 seq，[ack] 按 seq 精确移除已上报前缀——
 * 即便上报在途时新样本把最旧样本挤掉（覆盖丢弃），ack 也只移除仍在缓冲、seq 不大于上报末样本者，
 * 绝不误删更新的样本。
 */
class MetricSampleBuffer(
    private val capacity: Int,
    private val bucketMs: Long = DEFAULT_BUCKET_MS,
) {
    /** 带单调 seq 的样本槽（seq 用于上报确认时精确定位已上报前缀）。 */
    private data class Seqd(val seq: Long, val sample: MetricSample)

    private val lock = Any()
    private val items = ArrayDeque<Seqd>()
    private var nextSeq = 0L

    /** 上次成功上报以来被环形覆盖丢弃的样本数（§4.2 droppedSinceLast）。 */
    private var droppedSinceLast = 0L

    /** 当前缓冲样本数（测试 / 观测用）。 */
    fun size(): Int = synchronized(lock) { items.size }

    /** 当前累计被覆盖丢弃数（测试 / 观测用）。 */
    fun droppedCount(): Long = synchronized(lock) { droppedSinceLast }

    /** 追加一条样本；容量满则覆盖最旧并累加丢弃计数。 */
    fun add(sample: MetricSample) {
        synchronized(lock) {
            if (items.size >= capacity) {
                items.removeFirst()
                droppedSinceLast++
            }
            items.addLast(Seqd(nextSeq++, sample))
        }
    }

    /**
     * 取「已闭合桶」的样本快照供上报（不移除），最多 [maxBuckets] 个桶。
     *
     * @param nowMs 当前采样时钟；`bucketStart + bucketMs <= nowMs` 的桶视为已闭合可上报，当前桶保留。
     * @return 快照（已闭合样本升序、末样本 seq、随本批上报的丢弃计数）；无可上报样本时 samples 为空、lastSeq=-1。
     */
    fun peekClosed(
        nowMs: Long,
        maxBuckets: Int = DEFAULT_MAX_BUCKETS,
    ): ClosedSnapshot {
        synchronized(lock) {
            val taken = ArrayList<MetricSample>()
            var lastSeq = -1L
            var buckets = 0
            var currentBucket = Long.MIN_VALUE
            for (slot in items) {
                val bucket = bucketStart(slot.sample.tsMs)
                val isNewBucket = bucket != currentBucket
                // 停止条件（样本按时间升序入队，遇首个不满足者其后皆不满足）：
                // ① 桶未闭合（bucket + bucketMs > nowMs，当前采集桶保留不上报）；② 已达单批桶数上限且遇到新桶。
                val closed = bucket + bucketMs <= nowMs
                if (!closed || (isNewBucket && buckets >= maxBuckets)) break
                if (isNewBucket) {
                    buckets++
                    currentBucket = bucket
                }
                taken.add(slot.sample)
                lastSeq = slot.seq
            }
            return ClosedSnapshot(samples = taken, lastSeq = lastSeq, droppedSinceLast = droppedSinceLast)
        }
    }

    /**
     * 上报成功确认：移除 seq 不大于 [lastSeq] 的样本（已上报前缀），并扣减随本批上报的丢弃计数
     * [reportedDropped]（上报在途新产生的丢弃保留到下批）。lastSeq < 0（空快照）为 no-op。
     */
    fun ack(
        lastSeq: Long,
        reportedDropped: Long,
    ) {
        if (lastSeq < 0L) return
        synchronized(lock) {
            while (items.isNotEmpty() && items.first().seq <= lastSeq) {
                items.removeFirst()
            }
            droppedSinceLast = (droppedSinceLast - reportedDropped).coerceAtLeast(0L)
        }
    }

    private fun bucketStart(tsMs: Long): Long = tsMs - Math.floorMod(tsMs, bucketMs)

    companion object {
        /** 默认 5s 桶宽（毫秒）。 */
        const val DEFAULT_BUCKET_MS: Long = 5_000L

        /** 默认单批最大桶数（协议 §5.1 单批 ≤120）。 */
        const val DEFAULT_MAX_BUCKETS: Int = 120
    }
}

/**
 * 上报快照：已闭合桶样本（升序）+ 末样本 seq + 随本批上报的丢弃计数。
 *
 * @param samples          可上报的已闭合样本（升序）
 * @param lastSeq          末样本 seq；上报成功后据此 [MetricSampleBuffer.ack]
 * @param droppedSinceLast 上次成功上报以来被覆盖丢弃数（随本批上报，§4.2）
 */
data class ClosedSnapshot(
    val samples: List<MetricSample>,
    val lastSeq: Long,
    val droppedSinceLast: Long,
)
