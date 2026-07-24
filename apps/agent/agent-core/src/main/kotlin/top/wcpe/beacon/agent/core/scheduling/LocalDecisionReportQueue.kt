package top.wcpe.beacon.agent.core.scheduling

import top.wcpe.beacon.agent.core.client.LocalDecisionReport

/**
 * 降级期本地决策的补报队列（FR-148 §4.6 / §8 待定 7）：有界内存队列，满丢最旧，best-effort。
 *
 * 降级决策入队（[offer]），控制面恢复后由刷新循环 [drain] 批量补报（report-local）。仅内存不落盘——
 * 降级期决策的可查性让位于实现简单性（§8 待定 7）。线程安全（offer 在决策线程、drain 在刷新线程）。
 *
 * @param capacity 队列容量（默认 512，写满丢最旧）
 */
class LocalDecisionReportQueue(
    private val capacity: Int = DEFAULT_CAPACITY,
) {
    private val lock = Any()
    private val deque = ArrayDeque<LocalDecisionReport>()

    /** 入队一条补报记录；写满则先丢最旧再入（best-effort，§8 待定 7）。 */
    fun offer(report: LocalDecisionReport) {
        synchronized(lock) {
            if (deque.size >= capacity) {
                deque.removeFirst()
            }
            deque.addLast(report)
        }
    }

    /** 取走当前全部记录并清空（恢复后一次补报）；无记录返回空表。 */
    fun drain(): List<LocalDecisionReport> =
        synchronized(lock) {
            if (deque.isEmpty()) {
                return emptyList()
            }
            val copy = deque.toList()
            deque.clear()
            copy
        }

    /** 当前积压条数（供测试 / 观测）。 */
    fun size(): Int = synchronized(lock) { deque.size }

    private companion object {
        /** 补报队列容量（FR-148 §4.6，512 条、满丢最旧）。 */
        const val DEFAULT_CAPACITY = 512
    }
}
