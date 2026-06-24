package top.wcpe.beacon.agent.core.log

import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import java.io.File

/**
 * 把 agent 自身日志旁路进 [AgentLogBuffer] 的 [PlatformAdapter] 装饰器（FR-88，见 ADR-0040）。
 *
 * 包裹壳层真实 adapter：[info] / [warn] / [error] 先委托原 adapter 照常打日志，
 * 再把「级别 + 文本」追加进环形缓冲（落缓冲时由 buffer 内部脱敏）。其余非日志能力一律透传。
 *
 * 这样所有经 core 的日志自动入缓冲，**壳层日志实现零改动**——core 装配时用本类包裹壳传入的 adapter 即可。
 *
 * @param delegate 被包裹的真实平台适配器（壳层实现）
 * @param buffer   自身日志环形缓冲（追加目标）
 */
class BufferingPlatformAdapter(
    private val delegate: PlatformAdapter,
    private val buffer: AgentLogBuffer,
) : PlatformAdapter {

    // ===== 日志：先照常打，再旁路入缓冲（buffer 内部脱敏）=====

    override fun info(msg: String) {
        delegate.info(msg)
        buffer.append("INFO", msg)
    }

    override fun warn(msg: String) {
        delegate.warn(msg)
        buffer.append("WARN", msg)
    }

    override fun error(msg: String, t: Throwable?) {
        delegate.error(msg, t)
        // 异常信息也入缓冲（拼到文本里，脱敏在 buffer 内统一做）；异常类名 + 消息有助排障。
        val text = if (t != null) "$msg：${t.javaClass.simpleName}: ${t.message ?: "无错误信息"}" else msg
        buffer.append("ERROR", text)
    }

    // ===== 其余能力：原样透传 =====

    override fun runAsync(task: () -> Unit) = delegate.runAsync(task)

    override fun runAsyncDelayed(delayMs: Long, task: () -> Unit) = delegate.runAsyncDelayed(delayMs, task)

    override fun runSync(task: () -> Unit) = delegate.runSync(task)

    override fun dataFolder(): File = delegate.dataFolder()

    override fun pluginsBaseFolder(): File = delegate.pluginsBaseFolder()

    override fun readPluginsTree(): Map<String, ByteArray> = delegate.readPluginsTree()

    override fun readPluginsTreeMetadata(): Map<String, Long> = delegate.readPluginsTreeMetadata()

    override fun publishConfigChanged(changed: Set<String>, newMd5: String) =
        delegate.publishConfigChanged(changed, newMd5)

    override fun dispatchConsoleCommand(command: String) = delegate.dispatchConsoleCommand(command)
}
