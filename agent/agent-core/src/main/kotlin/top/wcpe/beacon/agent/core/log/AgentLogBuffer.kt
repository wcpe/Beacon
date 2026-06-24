package top.wcpe.beacon.agent.core.log

/**
 * agent 自身日志的一行（级别 + 脱敏后文本，FR-88，见 ADR-0040）。
 *
 * @param level 日志级别（INFO / WARN / ERROR）
 * @param text  脱敏后的日志文本（入缓冲前已经 [LogRedactor] 处理，绝不含原始敏感串）
 */
data class LogLine(
    val level: String,
    val text: String,
)

/**
 * agent 自身日志的**有界内存环形缓冲**（FR-88，见 ADR-0040）。线程安全、O(1) 追加。
 *
 * 只持有 agent 自己经 [top.wcpe.beacon.agent.core.platform.PlatformAdapter] 打的日志行——
 * **绝不打开 / 读取任何磁盘日志文件**（不读 server `logs/latest.log`、不读 plugins/ 下任何文件）。
 * 即便控制面被攻破，从这里能拿到的上限就是「agent 进程近期打的、已脱敏的日志行」。
 *
 * 落缓冲那一刻即经 [LogRedactor] 脱敏（[append] 内完成），缓冲里存的就已是脱敏文本，
 * 回传链路任何环节都拿不到原文。
 *
 * 容量有界：满则挤出最旧（FIFO 环形语义）。读写经同一把锁守护，可在任意线程并发调用
 * （日志可能在 MC 主线程打，追加是纯内存 O(1)、不阻塞；读快照 + HTTP 回传仍只在 async 线程，见 ADR-0040 决策6）。
 *
 * @param capacity 最多保留的日志行数（有界；满则挤出最旧）
 */
class AgentLogBuffer(private val capacity: Int) {

    init {
        require(capacity > 0) { "环形缓冲容量必须为正：$capacity" }
    }

    /** 守护读写的锁（缓冲是低频小对象，单锁足矣，不引入并发集合复杂度）。 */
    private val lock = Any()

    /** 底层存储：双端队列，尾部追加、头部挤出最旧。容量满时先 removeFirst 再 addLast。 */
    private val lines = ArrayDeque<LogLine>(capacity)

    /**
     * 追加一行日志：**落缓冲前先脱敏**，满则挤出最旧。
     *
     * @param level 日志级别（INFO / WARN / ERROR）
     * @param text  原始日志文本（本方法内脱敏，调用方无需预处理）
     */
    fun append(level: String, text: String) {
        val redacted = LogRedactor.redact(text)
        synchronized(lock) {
            if (lines.size >= capacity) {
                lines.removeFirst()
            }
            lines.addLast(LogLine(level, redacted))
        }
    }

    /**
     * 取当前缓冲快照（最旧 → 最新）。返回不可变拷贝，调用方持有期间缓冲继续追加互不影响。
     */
    fun snapshot(): List<LogLine> {
        synchronized(lock) {
            return ArrayList(lines)
        }
    }
}
