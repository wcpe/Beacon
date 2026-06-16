package top.wcpe.beacon.agent.core.platform

import java.io.File

/**
 * 平台适配接口：把 TabooLib / Bukkit / Bungee 的调度、数据目录、事件派发、日志能力注入 core。
 *
 * core 定义、壳实现（依赖倒置），打破「core 想用平台能力」的潜在反向依赖。
 * 所有 HTTP / 文件 IO 由 core 经 runAsync / runAsyncDelayed 落到异步线程，绝不阻塞 MC 主线程。
 */
interface PlatformAdapter {

    /** 异步执行任务（后台线程）。 */
    fun runAsync(task: () -> Unit)

    /** 延迟 delayMs 毫秒后异步执行（退避重连用，不 sleep 占线程）。 */
    fun runAsyncDelayed(delayMs: Long, task: () -> Unit)

    /** 切回主线程执行极短任务（仅用于需主线程的事件派发）。 */
    fun runSync(task: () -> Unit)

    /** agent 数据目录（快照、有效配置落点）。 */
    fun dataFolder(): File

    /**
     * 插件 plugins 基目录（文件树托管镜像落盘根，通道B）。
     *
     * 默认取 agent 数据目录的父目录（agent 自身在 plugins/<本插件>/ 下，父级即 plugins）。
     * 壳层可按平台覆盖。
     */
    fun pluginsBaseFolder(): File = dataFolder().parentFile ?: dataFolder()

    /** 广播「配置已更新」给同进程业务插件（平台各自实现事件派发）。 */
    fun publishConfigChanged(changed: Set<String>, newMd5: String)

    /** INFO 级日志。 */
    fun info(msg: String)

    /** WARN 级日志。 */
    fun warn(msg: String)

    /** ERROR 级日志，可附异常。 */
    fun error(msg: String, t: Throwable?)
}
