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
     *
     * 注意：必须先 [File.getAbsoluteFile] 再取父级——TabooLib 的 getDataFolder() 在部分平台返回
     * **相对路径**（如 `plugins/<本插件>`），此时 `File("plugins/x").parentFile` 为 `File("plugins")`、
     * 而 `File("plugins").parentFile` 为 null；若下游再以「基目录父级 + plugins/<目标>」解析覆盖落盘根，
     * 相对路径会令父级回退成 plugins 本身、最终把覆盖文件落到 `plugins/plugins/<目标>`（重复 plugins）。
     * 先绝对化即可让父级稳定为服务器根，避免该路径重复缺陷（FR-14/FR-15 镜像落盘与路径限定共用此基目录）。
     */
    fun pluginsBaseFolder(): File = dataFolder().absoluteFile.parentFile ?: dataFolder().absoluteFile

    /**
     * 读取真实 plugins 目录整棵子树为「相对路径（正斜杠） → 原始字节」映射（反向抓取，FR-39，见 ADR-0027）。
     *
     * 只读、不写盘；**仅在 async 线程调用**（读盘是阻塞 IO，绝不上 MC 主线程）。
     * 读取根 = [pluginsBaseFolder]（agent dataFolder 的父目录）；FS 级安全（Path 容纳 + 符号链接逃逸判定）
     * 由实现负责（限死真实 plugins 根内，不随符号链接逃逸）。返回原始字节，文本/二进制判别与上限校验
     * 交 core 纯函数 [top.wcpe.beacon.agent.core.command.PluginsTreeFilter] 统一处理。
     *
     * 默认空实现：未实现读盘的平台 / 测试桩返回空映射（反向抓取能力不上线）。壳层各自实现平台 IO。
     */
    fun readPluginsTree(): Map<String, ByteArray> = emptyMap()

    /**
     * 扫描真实 plugins 目录树的**元信息**为「相对路径（正斜杠） → 文件字节大小」映射（反向抓取 scan 阶段，FR-58，见 ADR-0037）。
     *
     * 与 [readPluginsTree] 的根本区别：**只 `stat` 取大小、绝不读取文件内容、绝不因任何文件超大而失败**——
     * 治根超限运行时垃圾击穿（让其在清单里被看见、由人决定纳入/排除）。只读、不写盘；**仅在 async 线程调用**。
     * FS 级安全（Path 容纳 + 符号链接逃逸判定）由实现负责，size 的 overThreshold 红标由 core 纯函数
     * [top.wcpe.beacon.agent.core.command.PluginsTreeFilter.scan] 据 size 判定。
     *
     * 默认空实现：未实现的平台 / 测试桩返回空映射（scan 能力不上线）。壳层各自实现平台 IO。
     */
    fun readPluginsTreeMetadata(): Map<String, Long> = emptyMap()

    /** 广播「配置已更新」给同进程业务插件（平台各自实现事件派发）。 */
    fun publishConfigChanged(changed: Set<String>, newMd5: String)

    /**
     * 派发一条受限控制台命令（三方插件文件覆盖兼容的重载命令，FR-15）。
     *
     * 壳实现：Bukkit 走 `Bukkit.dispatchCommand(consoleSender, ...)`、Bungee 走对应 ProxyServer API；
     * **core 与适配器一律不引入任何进程 / shell 执行 API（无 Runtime.exec / ProcessBuilder）**——物理上无法落到 OS shell（ADR-0011 决策 2）。
     *
     * 命令是否真正派发由 core 侧白名单 + 注入校验把关；本方法只负责把已校验的单条命令交给平台。
     * 默认空实现：未实现命令派发的平台 / 测试桩不动作（命令执行能力不上线，符合 ADR-0009 gate 在鉴权之后）。
     *
     * 派发**不在 MC 主线程同步等结果**（很多 reload 主线程阻塞）：壳层要么只派发不等结果，
     * 要么显式接受「重载命令可能造成主线程卡顿」，二选一并写清（ADR-0011 决策 6）。
     */
    fun dispatchConsoleCommand(command: String) {
        // 默认不动作：命令派发是 FR-15 的高风险能力，未显式实现的平台不开放。
    }

    /** INFO 级日志。 */
    fun info(msg: String)

    /** WARN 级日志。 */
    fun warn(msg: String)

    /** ERROR 级日志，可附异常。 */
    fun error(msg: String, t: Throwable?)
}
