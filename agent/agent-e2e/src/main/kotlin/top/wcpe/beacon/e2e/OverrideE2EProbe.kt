package top.wcpe.beacon.e2e

import taboolib.common.platform.ProxyCommandSender
import taboolib.common.platform.command.command
import taboolib.common.platform.function.getDataFolder
import taboolib.common.platform.function.info
import taboolib.common.platform.function.submit
import java.io.File
import java.util.concurrent.atomic.AtomicReference

/**
 * Beacon FR-15 三方覆盖 + 受限重载命令的端到端验收探针（Bukkit 壳）。
 *
 * 职责：把本验收插件自身的数据目录 plugins/BeaconE2E/ 当作「被覆盖的三方插件目录」，
 * 观测 agent 对覆盖集（ADR-0011）的真机行为，供外部驱动断言：
 *  1. 启用时种下「原文件」managed.yml（内容 A），作为将被覆盖、需备份的原件；
 *  2. 注册控制台命令 beacone2ereload（即覆盖集的受限重载命令）——agent 派发它时本探针记
 *     「命令收到 + 收到时 managed.yml 的磁盘 md5」，据此证明「落盘成功后才派发命令」的次序；
 *  3. 轮询 managed.yml 的内容 md5，变更即记「文件被改」。
 *
 * 所有观测追加写到数据目录下的标记文件 e2e-override-observations.log（格式见 [E2EObservation]），
 * 与配置观测（BeaconE2EBukkit 的 e2e-observations.log）分离，互不干扰。
 *
 * 注意：本探针只读磁盘 + 写自己的标记文件，不触碰 agent 覆盖逻辑；它验证已接线的覆盖链路与
 * ADR-0011 安全不变量是否在真机上成立，绝不为「让断言通过」而旁路任何安全约束。
 */
object OverrideE2EProbe {

    /** 被覆盖的目标文件名（相对覆盖集 targetRoot=plugins/BeaconE2E，落本插件数据目录）。 */
    private const val TARGET_FILE = "managed.yml"

    /** 覆盖观测标记文件名：外部驱动据此断言。 */
    private const val OBSERVATION_FILE = "e2e-override-observations.log"

    /** 种下的原文件内容（内容 A）：将被覆盖集覆盖为新内容（内容 B），覆盖前应被 agent 备份。 */
    private const val INITIAL_CONTENT = "marker: original-A\n"

    /** 覆盖集的受限重载命令名（须与控制面 override-set 的 reloadCommand 首 token 一致）。 */
    private const val RELOAD_COMMAND = "beacone2ereload"

    /** 轮询周期（tick，20 tick/秒）：约每 1 秒读一次 managed.yml 现状。 */
    private const val POLL_INTERVAL_TICKS = 20L

    /** 上次观测到的 managed.yml 内容 md5，用于轮询去重（仅在「内容真正变化」时记一条 FILE_CHANGED）。 */
    private val lastMd5 = AtomicReference<String?>(null)

    /**
     * 启动探针：清旧标记 → 种原文件 → 注册受限重载命令 → 启异步文件轮询。
     * 由 BeaconE2EBukkit 在 ENABLE 阶段调用（与 BeaconAgent 注册命令同款时机）。
     */
    fun start() {
        val markFile = File(getDataFolder(), OBSERVATION_FILE)
        // 清空上轮残留，保证每次 run 的标记文件只含本轮观测。
        if (markFile.exists()) {
            markFile.delete()
        }

        // 种下原文件 managed.yml（内容 A）：仅当不存在时种，避免覆盖 agent 本轮已落的内容。
        val target = File(getDataFolder(), TARGET_FILE)
        if (!target.exists()) {
            target.parentFile?.mkdirs()
            target.writeText(INITIAL_CONTENT, Charsets.UTF_8)
        }
        val seedMd5 = E2EObservation.md5Hex(target.readBytes())
        // 以现状 md5 作轮询基准：种下的原件不记为 FILE_CHANGED，只有后续被覆盖才记。
        lastMd5.set(seedMd5)
        E2EObservation.append(markFile, "SEED", TARGET_FILE, seedMd5, target.readText(Charsets.UTF_8))
        info("Beacon E2E 覆盖探针已种原文件 $TARGET_FILE（md5=$seedMd5），标记文件=${markFile.absolutePath}")

        registerReloadCommand(markFile)
        startWatching(markFile)
    }

    /**
     * 注册受限重载命令 beacone2ereload。
     *
     * agent 经控制台派发本命令时，回调读取 managed.yml 当下磁盘 md5 + 内容并记一条 COMMAND_RECEIVED——
     * 若记录到的磁盘 md5 等于覆盖后的新内容 md5，即证明「文件先落盘、命令后派发」的次序（ADR-0011）。
     */
    private fun registerReloadCommand(markFile: File) {
        command(RELOAD_COMMAND, permission = "beacon.e2e.reload") {
            execute<ProxyCommandSender> { _, _, _ ->
                val target = File(getDataFolder(), TARGET_FILE)
                val (md5, raw) = if (target.exists()) {
                    val bytes = target.readBytes()
                    E2EObservation.md5Hex(bytes) to String(bytes, Charsets.UTF_8)
                } else {
                    "-" to "（命令收到时目标文件不存在）"
                }
                E2EObservation.append(markFile, "COMMAND_RECEIVED", TARGET_FILE, md5, raw)
                info("Beacon E2E 覆盖探针收到受限重载命令 $RELOAD_COMMAND（此刻 $TARGET_FILE md5=$md5）")
            }
        }
        info("Beacon E2E 覆盖探针已注册受限重载命令 $RELOAD_COMMAND")
    }

    /** 周期性轮询 managed.yml 内容 md5；与上次不同即记一条 FILE_CHANGED（去重以 md5 为准）。 */
    private fun startWatching(markFile: File) {
        submit(async = true, delay = POLL_INTERVAL_TICKS, period = POLL_INTERVAL_TICKS) {
            val target = File(getDataFolder(), TARGET_FILE)
            if (!target.exists()) {
                return@submit
            }
            val bytes = target.readBytes()
            val md5 = E2EObservation.md5Hex(bytes)
            if (lastMd5.getAndSet(md5) != md5) {
                E2EObservation.append(markFile, "FILE_CHANGED", TARGET_FILE, md5, String(bytes, Charsets.UTF_8))
            }
        }
    }
}
