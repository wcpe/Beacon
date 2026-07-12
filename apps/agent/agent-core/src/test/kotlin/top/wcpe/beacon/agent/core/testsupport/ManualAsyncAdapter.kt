package top.wcpe.beacon.agent.core.testsupport

import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import java.io.File

/**
 * 测试用手动调度适配器：runAsync 同步执行、runAsyncDelayed 只入队（测试显式 [drainOne] 推进），
 * 捕获各级日志供 warn-once 断言。供连接采集 / 跨服消息等「代」循环协调器的确定性单测复用。
 */
class ManualAsyncAdapter(private val folder: File = File(".")) : PlatformAdapter {
    val delayed = ArrayDeque<() -> Unit>()
    val infos = mutableListOf<String>()
    val warns = mutableListOf<String>()

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

    override fun dataFolder(): File = folder

    override fun publishConfigChanged(
        changed: Set<String>,
        newMd5: String,
    ) = Unit

    override fun info(msg: String) {
        infos.add(msg)
    }

    override fun warn(msg: String) {
        warns.add(msg)
    }

    override fun error(
        msg: String,
        t: Throwable?,
    ) = Unit

    /** 推进一个延迟任务（下一次 tick）。无任务则抛异常（暴露测试预期偏差）。 */
    fun drainOne() {
        delayed.removeFirst().invoke()
    }

    /** 当前排队的延迟任务数。 */
    fun delayedCount(): Int = delayed.size
}
