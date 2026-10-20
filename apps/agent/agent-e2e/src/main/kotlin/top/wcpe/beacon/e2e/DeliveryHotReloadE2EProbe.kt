package top.wcpe.beacon.e2e

import taboolib.common.platform.function.getDataFolder
import taboolib.common.platform.function.info
import taboolib.common.platform.function.submit
import top.wcpe.beacon.agent.api.BeaconAgentProvider
import top.wcpe.beacon.agent.api.ConfigChangeListener
import top.wcpe.beacon.agent.api.ConfigReloadResult
import top.wcpe.beacon.agent.api.ListenerHandle
import top.wcpe.beacon.agent.api.ReportingConfigChangeListener
import java.io.File
import java.util.concurrent.atomic.AtomicLong

/**
 * 重载结果上报（FR-273）真机验收探针。
 *
 * 作为「业务插件」订阅 BeaconAgent 的配置变更，按**各自路径**返回不同结果，
 * 以在真实 Paper 上复现验收清单 A2–A6 与 B1/B2：
 *
 * | 路径后缀              | 行为                                   | 对应用例 |
 * |----------------------|----------------------------------------|---------|
 * | `delivery-reload-ok.yml`      | 返回 ok + 自报 md5/版本/条数     | A1      |
 * | `delivery-reload-fail.yml`    | 返回 failed + 可读原因            | A2      |
 * | `delivery-reload-throw.yml`   | 抛 RuntimeException               | A6      |
 * | `delivery-reload-error.yml`   | 抛 NoClassDefFoundError（Error）  | A4      |
 * | `delivery-reload-hang.yml`    | 睡眠远超超时值                     | A3      |
 * | `delivery-reload-null.yml`    | 返回 null（缺结果）                | A5      |
 * | `delivery-reload-legacy.yml`  | 走**旧 void 回调**（老插件路径）   | A9      |
 *
 * 关键设计：一次派发里有多条路径同时变更时，每个路径独立返回各自结果——
 * 这正是 A6「一个插件失败不影响其余插件」要验证的隔离性。
 */
object DeliveryHotReloadE2EProbe {
    const val OK_PATH = "plugins/BeaconE2E/delivery-reload-ok.yml"
    const val FAIL_PATH = "plugins/BeaconE2E/delivery-reload-fail.yml"
    const val THROW_PATH = "plugins/BeaconE2E/delivery-reload-throw.yml"
    const val ERROR_PATH = "plugins/BeaconE2E/delivery-reload-error.yml"
    const val HANG_PATH = "plugins/BeaconE2E/delivery-reload-hang.yml"
    const val NULL_PATH = "plugins/BeaconE2E/delivery-reload-null.yml"
    const val LEGACY_PATH = "plugins/BeaconE2E/delivery-reload-legacy.yml"

    /** 兼容既有 hot_reload 用例（FR-171）沿用的两个路径名。 */
    const val SUCCESS_PATH = OK_PATH
    const val FAILURE_PATH = FAIL_PATH

    const val OBSERVATION_FILE = "e2e-delivery-hot-reload.log"

    private const val INITIAL_CONTENT = "marker: original\n"
    private const val POLL_INTERVAL_TICKS = 20L
    private const val ALIVE_INTERVAL = 5L

    /** 睡眠时长：需明显超过 agent 侧 `delivery.config-reload-timeout-ms`（真机取 3000ms）。 */
    private const val HANG_MS = 60_000L

    private val WATCHED =
        listOf(OK_PATH, FAIL_PATH, THROW_PATH, ERROR_PATH, HANG_PATH, NULL_PATH, LEGACY_PATH)

    @Volatile
    private var reportingHandle: ListenerHandle? = null

    @Volatile
    private var legacyHandle: ListenerHandle? = null

    /** A5 专用：Java 侧「返回 null」实现，Kotlin 无法表达。 */
    @Volatile
    private var nullHandle: ListenerHandle? = null

    private val polls = AtomicLong(0)

    /** 清理上轮观测、种下各路径原始配置，并启动上报式 + 遗留式两条监听与存活探针。 */
    fun start() {
        val observation = File(getDataFolder(), OBSERVATION_FILE)
        if (observation.exists()) observation.delete()
        WATCHED.forEach { seed(it) }
        E2EObservation.append(observation, "PROBE_START", "-", "-", "业务插件探针已启动")
        register(observation)
        submit(async = true, delay = POLL_INTERVAL_TICKS, period = POLL_INTERVAL_TICKS) {
            register(observation)
            if (polls.incrementAndGet() % ALIVE_INTERVAL == 0L) {
                E2EObservation.append(observation, "PROBE_ALIVE", "-", "-", "Paper 业务插件仍在线")
            }
        }
    }

    /** 注销两条监听器。 */
    fun stop() {
        reportingHandle?.remove()
        reportingHandle = null
        legacyHandle?.remove()
        legacyHandle = null
        nullHandle?.remove()
        nullHandle = null
    }

    /** Agent 就绪后注册两条监听：上报式（新契约）与 void（老插件路径）。 */
    private fun register(observation: File) {
        if (!BeaconAgentProvider.isAvailable()) return
        if (reportingHandle == null) {
            reportingHandle =
                BeaconAgentProvider.get().config().onChange(
                    object : ReportingConfigChangeListener {
                        override fun onConfigChangedReporting(
                            changed: Set<String>,
                            newMd5: String,
                        ): ConfigReloadResult? = onReportingChanged(observation, changed, newMd5)
                    },
                )
            E2EObservation.append(observation, "REPORTING_READY", "-", "-", "已注册上报式监听")
        }
        if (legacyHandle == null) {
            legacyHandle =
                BeaconAgentProvider.get().config().onChange(
                    ConfigChangeListener { changed, md5 -> onLegacyChanged(observation, changed, md5) },
                )
            E2EObservation.append(observation, "LEGACY_READY", "-", "-", "已注册遗留式(void)监听")
        }
        if (nullHandle == null) {
            // A5：Java 侧实现，对受控路径显式返回 null（Kotlin 侧无法表达非空契约的 null）。
            nullHandle = BeaconAgentProvider.get().config().onChange(NullResultReloadProbe())
            E2EObservation.append(observation, "NULL_PROBE_READY", "-", "-", "已注册返回 null 的监听")
        }
        info("Beacon E2E 已注册 FR-273 重载结果上报探针")
    }

    /**
     * 上报式回调：**按首个命中的受控路径决定本次结果**，其余路径仅留证。
     *
     * 一次派发只返回一个结果（契约如此），因此按固定优先级取首个命中的受控路径；
     * 测试用例通过「只改一个受控文件」保证行为可预期。遗留路径由另一条 void 监听独立处理，
     * 故 A9（老插件）与上报式行为互不干扰。
     */
    private fun onReportingChanged(
        observation: File,
        changed: Set<String>,
        newMd5: String,
    ): ConfigReloadResult {
        val path =
            WATCHED.firstOrNull { it != LEGACY_PATH && it != NULL_PATH && changed.contains(it) }
                ?: return ConfigReloadResult.ok().withAppliedMd5(newMd5)
        val target = target(path)
        val raw = if (target.exists()) target.readText(Charsets.UTF_8) else "（目标文件不存在）"
        val md5 = if (target.exists()) E2EObservation.md5Hex(target.readBytes()) else newMd5
        E2EObservation.append(observation, "ON_CHANGE", path, md5, raw)
        return when (path) {
            OK_PATH -> {
                E2EObservation.append(observation, "REPORTING_OK", path, md5, "上报成功并自报加载事实")
                ConfigReloadResult.ok()
                    .withAppliedMd5(newMd5)
                    .withAppliedVersion("v-e2e")
                    .withAppliedCount(7)
            }
            FAIL_PATH -> {
                E2EObservation.append(observation, "REPORTING_FAILED", path, md5, "业务插件拒绝本次热更新")
                ConfigReloadResult.failed("FR-273 E2E 业务插件拒绝失败路径热更新")
            }
            THROW_PATH -> {
                E2EObservation.append(observation, "REPORTING_THROW", path, md5, "将抛 RuntimeException")
                throw RuntimeException("FR-273 E2E 业务插件故意抛异常")
            }
            ERROR_PATH -> {
                E2EObservation.append(observation, "REPORTING_ERROR", path, md5, "将抛 NoClassDefFoundError")
                // 刻意抛 Error 而非 Exception：验证 agent 侧 catch Throwable。
                throw NoClassDefFoundError("top/wcpe/beacon/e2e/AcceptanceProbeAbsent")
            }
            HANG_PATH -> {
                E2EObservation.append(observation, "REPORTING_HANG", path, md5, "将睡眠超过重载超时")
                sleepQuietly(HANG_MS)
                ConfigReloadResult.ok().withAppliedMd5(newMd5)
            }
            NULL_PATH -> {
                E2EObservation.append(observation, "REPORTING_NULL", path, md5, "返回 null 结果（Java 侧实现）")
                // 契约签名非空，Kotlin 无法返回 null；A5 的 null 分支由 Java 侧
                // LegacyNullResultProbe 承担（见 apps/agent/agent-e2e/src/main/java）。
                ConfigReloadResult.ok().withAppliedMd5(newMd5)
            }
            else -> ConfigReloadResult.ok().withAppliedMd5(newMd5)
        }
    }

    /** 遗留 void 回调（老插件路径，A9）：只留证，不返回结果。 */
    private fun onLegacyChanged(
        observation: File,
        changed: Set<String>,
        newMd5: String,
    ) {
        if (!changed.contains(LEGACY_PATH)) return
        val target = target(LEGACY_PATH)
        val raw = if (target.exists()) target.readText(Charsets.UTF_8) else "（目标文件不存在）"
        val md5 = if (target.exists()) E2EObservation.md5Hex(target.readBytes()) else newMd5
        E2EObservation.append(observation, "ON_CHANGE_LEGACY", LEGACY_PATH, md5, raw)
        E2EObservation.append(observation, "LEGACY_DONE", LEGACY_PATH, md5, "老式 void 回调执行完毕")
    }

    private fun sleepQuietly(ms: Long) {
        try {
            Thread.sleep(ms)
        } catch (e: InterruptedException) {
            Thread.currentThread().interrupt()
        }
    }

    /** 仅当文件不存在时种原始内容，避免覆盖 Agent 已落盘事实。 */
    private fun seed(path: String) {
        val file = target(path)
        if (!file.exists()) {
            file.parentFile?.mkdirs()
            file.writeText(INITIAL_CONTENT, Charsets.UTF_8)
        }
    }

    /** 把固定服务器根相对路径映射到本业务插件数据目录。 */
    private fun target(path: String): File = File(getDataFolder(), path.substringAfterLast('/'))
}
