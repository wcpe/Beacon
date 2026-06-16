package top.wcpe.beacon.agent.adapters.testutil

import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import java.io.File

/**
 * 测试用 PlatformAdapter：runAsync 同步执行（便于断言），记录 publishConfigChanged 调用。
 */
class RecordingPlatformAdapter(
    private val folder: File,
) : PlatformAdapter {

    /** 记录每次广播的（变更集, md5）。 */
    val published: MutableList<Pair<Set<String>, String>> = mutableListOf()

    override fun runAsync(task: () -> Unit) = task()

    override fun runAsyncDelayed(delayMs: Long, task: () -> Unit) = task()

    override fun runSync(task: () -> Unit) = task()

    override fun dataFolder(): File = folder

    override fun publishConfigChanged(changed: Set<String>, newMd5: String) {
        published.add(changed to newMd5)
    }

    override fun info(msg: String) {}

    override fun warn(msg: String) {}

    override fun error(msg: String, t: Throwable?) {}
}
