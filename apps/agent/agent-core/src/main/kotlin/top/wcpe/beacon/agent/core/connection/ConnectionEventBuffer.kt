package top.wcpe.beacon.agent.core.connection

/**
 * 连接事件有界缓冲（FR-145 §4.1 / §4.5 fail-static）。线程安全：采集线程 [add] 与上报线程 [peek]/[ack] 并发。
 *
 * 语义（仿 [MetricSampleBuffer][top.wcpe.beacon.agent.core.sampling.MetricSampleBuffer]）：
 * - 容量满、事件未及上报 → 覆盖**最旧**事件并 `droppedSinceLast++`（自上次成功上报以来被覆盖丢弃数）。
 *   控制面不可用时事件照常入缓冲、玩家进出服不受影响；溢出丢最旧并累计丢弃计数随下批上报（ADR-0057 可见）。
 * - [peek] 取最旧的至多 max 条（不移除）供上报；[ack] 上报成功后按 seq 精确移除已上报前缀。
 * - 仅内存、不跨重启落盘（spec 原文「本地有界缓冲」，YAGNI）。
 *
 * 用单调 seq 标记事件：即便上报在途时新事件把最旧事件挤掉（覆盖丢弃），[ack] 也只移除仍在缓冲、
 * seq 不大于上报末事件者，绝不误删更新的事件。
 */
class ConnectionEventBuffer(
    private val capacity: Int = DEFAULT_CAPACITY,
) {
    /** 带单调 seq 的事件槽。 */
    private data class Seqd(val seq: Long, val event: ConnectionEvent)

    private val lock = Any()
    private val items = ArrayDeque<Seqd>()
    private var nextSeq = 0L

    /** 上次成功上报以来被环形覆盖丢弃的事件数。 */
    private var droppedSinceLast = 0L

    /** 当前缓冲事件数（测试 / 观测用）。 */
    fun size(): Int = synchronized(lock) { items.size }

    /** 当前累计被覆盖丢弃数（测试 / 观测用）。 */
    fun droppedCount(): Long = synchronized(lock) { droppedSinceLast }

    /**
     * 追加一条事件；容量满则覆盖最旧并累加丢弃计数。
     *
     * @return true 表示追加后缓冲达到即时上报阈值（[FLUSH_THRESHOLD]），供采集侧触发一次即时上报（「满 200 条即上报」）。
     */
    fun add(event: ConnectionEvent): Boolean {
        synchronized(lock) {
            if (items.size >= capacity) {
                items.removeFirst()
                droppedSinceLast++
            }
            items.addLast(Seqd(nextSeq++, event))
            return items.size >= FLUSH_THRESHOLD
        }
    }

    /**
     * 取最旧的至多 [max] 条事件快照供上报（不移除）+ 末条 seq + 随本批上报的丢弃计数。
     *
     * @return 快照；缓冲空时 events 为空、lastSeq=-1。
     */
    fun peek(max: Int = DEFAULT_MAX_BATCH): ConnectionBatchSnapshot {
        synchronized(lock) {
            val taken = ArrayList<ConnectionEvent>(minOf(max, items.size))
            var lastSeq = -1L
            for (slot in items) {
                if (taken.size >= max) break
                taken.add(slot.event)
                lastSeq = slot.seq
            }
            return ConnectionBatchSnapshot(events = taken, lastSeq = lastSeq, droppedSinceLast = droppedSinceLast)
        }
    }

    /**
     * 上报成功确认：移除 seq 不大于 [lastSeq] 的事件（已上报前缀），并扣减随本批上报的丢弃计数
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

    companion object {
        /** 默认本地有界缓冲上限（FR-145 §4.5，10000 条；写满覆盖最旧）。 */
        const val DEFAULT_CAPACITY: Int = 10_000

        /** 默认单批上报上限（spec §5.1 单批 ≤500）。 */
        const val DEFAULT_MAX_BATCH: Int = 500

        /** 即时上报阈值（spec §4.1「满 200 条即上报」）。 */
        const val FLUSH_THRESHOLD: Int = 200
    }
}

/**
 * 连接批上报快照：最旧的至多 max 条事件 + 末条 seq + 随本批上报的丢弃计数。
 *
 * @param events 待上报事件（最旧优先）
 * @param lastSeq 末事件 seq；上报成功后据此 [ConnectionEventBuffer.ack]
 * @param droppedSinceLast 上次成功上报以来被覆盖丢弃数（随本批上报）
 */
data class ConnectionBatchSnapshot(
    val events: List<ConnectionEvent>,
    val lastSeq: Long,
    val droppedSinceLast: Long,
)
