package top.wcpe.beacon.agent.core.testutil

import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import java.io.File
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledExecutorService
import java.util.concurrent.TimeUnit

/**
 * 测试用 PlatformAdapter：runAsync / runAsyncDelayed 落到真实线程池，
 * 让生命周期的注册 / 循环并发真实发生（便于断言单飞不变量）。
 *
 * 注意：与 adapters 模块那个「同步执行」的 RecordingPlatformAdapter 不同——本适配器
 * 真异步，避免 AgentLifecycle 自调度循环在同步执行下无限递归栈溢出。
 */
class ThreadPoolPlatformAdapter(
    private val folder: File = File(System.getProperty("java.io.tmpdir")),
    threads: Int = 8,
) : PlatformAdapter {

    private val pool: ScheduledExecutorService = Executors.newScheduledThreadPool(threads)

    /** 记录每次广播的（变更集, md5）。 */
    val published: MutableList<Pair<Set<String>, String>> = java.util.Collections.synchronizedList(mutableListOf())

    override fun runAsync(task: () -> Unit) {
        pool.submit { task() }
    }

    override fun runAsyncDelayed(delayMs: Long, task: () -> Unit) {
        pool.schedule({ task() }, delayMs, TimeUnit.MILLISECONDS)
    }

    override fun runSync(task: () -> Unit) {
        task()
    }

    override fun dataFolder(): File = folder

    override fun publishConfigChanged(changed: Set<String>, newMd5: String) {
        published.add(changed to newMd5)
    }

    override fun info(msg: String) {}

    override fun warn(msg: String) {}

    override fun error(msg: String, t: Throwable?) {}

    /** 关停线程池并等待在飞任务自然结束（测试收尾用）。 */
    fun shutdown() {
        pool.shutdownNow()
        pool.awaitTermination(2, TimeUnit.SECONDS)
    }
}
