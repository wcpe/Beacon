package top.wcpe.beacon.e2e

import top.wcpe.beacon.agent.api.BeaconAgentProvider
import top.wcpe.beacon.agent.api.ListenerHandle
import taboolib.common.LifeCycle
import taboolib.common.platform.Awake
import taboolib.common.platform.Plugin
import taboolib.common.platform.function.getDataFolder
import taboolib.common.platform.function.info
import taboolib.common.platform.function.submit
import java.io.File
import java.security.MessageDigest
import java.time.OffsetDateTime
import java.util.concurrent.atomic.AtomicReference

/**
 * Beacon M6 端到端验收插件（BungeeCord 壳）。
 *
 * 与 Bukkit 版职责一致：作为「业务插件」经 agent 的纯 Java 只读 API 读约定 dataId，
 * 把每次观测追加写到数据目录下 e2e-observations.log，供外部驱动断言初始读取与热更。
 *
 * 注意：本类位于与本模块 group（top.wcpe.beacon.e2e）一致的根包下，TabooLib @Awake 生命周期扫描以
 * group 推导的根包为基准；放在其它根包将不被扫描、生命周期不触发。
 */
object BeaconE2EBungee : Plugin() {

    /** 约定观测的 dataId（与控制面侧 REST 建立的配置项一致）。 */
    private const val WATCH_DATA_ID = "beacon-e2e.yml"

    /** 标记文件名：外部驱动据此断言。 */
    private const val OBSERVATION_FILE = "e2e-observations.log"

    /** 轮询周期（tick，20 tick/秒）：约每 2 秒读一次有效配置。 */
    private const val POLL_INTERVAL_TICKS = 40L

    /** 上次已记录的 md5，用于轮询去重（仅在「值真正变化」时补记一条）。 */
    private val lastRecordedMd5 = AtomicReference<String?>(null)

    /** onChange 注销句柄；DISABLE 时释放。 */
    private var handle: ListenerHandle? = null

    @Awake(LifeCycle.ENABLE)
    fun enable() {
        val markFile = File(getDataFolder(), OBSERVATION_FILE)
        if (markFile.exists()) {
            markFile.delete()
        }
        append(markFile, "PLUGIN_ENABLE", WATCH_DATA_ID, "-", "（验收插件已启用，等待 agent 收敛）")
        info("Beacon E2E（代理）验收插件已启用，观测 dataId=$WATCH_DATA_ID，标记文件=${markFile.absolutePath}")
        registerChangeListener(markFile)
        startPolling(markFile)
    }

    @Awake(LifeCycle.DISABLE)
    fun disable() {
        handle?.remove()
        handle = null
    }

    /** 注册 onChange 监听；agent 尚未就绪时静默跳过，由轮询兜底重试注册。 */
    private fun registerChangeListener(markFile: File) {
        if (handle != null) {
            return
        }
        if (!BeaconAgentProvider.isAvailable()) {
            return
        }
        handle = BeaconAgentProvider.get().config().onChange { changedDataIds, newMd5 ->
            if (changedDataIds.contains(WATCH_DATA_ID)) {
                val raw = readRaw()
                lastRecordedMd5.set(perItemMd5OrWhole(raw, newMd5))
                append(markFile, "ON_CHANGE", WATCH_DATA_ID, perItemMd5OrWhole(raw, newMd5), raw ?: "（变更但读取为空）")
            }
        }
        info("Beacon E2E（代理）已注册有效配置变更监听")
    }

    /** 周期性轮询读取有效配置；首次读到与每次「值变化」均补记一条。 */
    private fun startPolling(markFile: File) {
        submit(async = true, delay = POLL_INTERVAL_TICKS, period = POLL_INTERVAL_TICKS) {
            registerChangeListener(markFile)
            if (!BeaconAgentProvider.isAvailable()) {
                return@submit
            }
            val raw = readRaw() ?: return@submit
            val md5 = itemMd5() ?: md5Hex(raw)
            if (lastRecordedMd5.getAndSet(md5) != md5) {
                append(markFile, "POLL", WATCH_DATA_ID, md5, raw)
            }
        }
    }

    /** 读取约定 dataId 的合并后原始文本；agent 未就绪或无该配置返回 null。 */
    private fun readRaw(): String? {
        if (!BeaconAgentProvider.isAvailable()) {
            return null
        }
        return BeaconAgentProvider.get().config().raw(WATCH_DATA_ID).orElse(null)
    }

    /** 读取约定 dataId 的单项 md5；不可用返回 null。 */
    private fun itemMd5(): String? {
        if (!BeaconAgentProvider.isAvailable()) {
            return null
        }
        return BeaconAgentProvider.get().config().md5(WATCH_DATA_ID).orElse(null)
    }

    /** onChange 回调里优先用单项 md5，缺失时退回整体 md5，再退回内容散列，保证记录非空。 */
    private fun perItemMd5OrWhole(raw: String?, wholeMd5: String): String {
        return itemMd5() ?: if (raw != null) md5Hex(raw) else wholeMd5
    }

    /** 计算文本 md5（小写 hex），仅用于轮询去重与记录展示。 */
    private fun md5Hex(text: String): String {
        val digest = MessageDigest.getInstance("MD5").digest(text.toByteArray(Charsets.UTF_8))
        return digest.joinToString("") { "%02x".format(it) }
    }

    /**
     * 向标记文件追加一行观测。格式：时间 | 来源 | dataId | md5 | raw（\n 转义为 \\n）。
     */
    @Synchronized
    private fun append(file: File, source: String, dataId: String, md5: String, raw: String) {
        file.parentFile?.mkdirs()
        val escaped = raw.replace("\\", "\\\\").replace("\n", "\\n").replace("\r", "")
        val line = "${OffsetDateTime.now()}|$source|$dataId|$md5|$escaped\n"
        file.appendText(line, Charsets.UTF_8)
    }
}
