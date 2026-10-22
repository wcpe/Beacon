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

    /**
     * FR-278「重载超时热生效」计时路径（两条，用于同一运行时下做 A/B 观测）。
     *
     * ## 为什么需要专门的路径
     *
     * 超时值本身不对外暴露，唯一能证明「运行时真的按新值工作」的事实是**它何时打断插件**。
     * 故这两条路径的回调会睡满 [TIMED_SLEEP_MS]（远超任何被测超时），并记录：
     *  - `elapsed`：从进入回调到离开（被中断或睡满）的毫秒数；
     *  - `interrupted`：是否被中断——为 true 即「派发器按超时预算把它掐了」。
     *
     * 于是 `elapsed` 就是**生效中的超时预算的实测值**：下发 2000 应量到 ~2000，
     * 再下发 12000 应量到 ~12000。若热生效没接线，第二次仍会量到 ~2000 之类，
     * 断言据此落地——这是「运行时行为」而非「缓存文件里有没有这个值」。
     */
    const val TIMED_A_PATH = "plugins/BeaconE2E/delivery-reload-timed-a.yml"
    const val TIMED_B_PATH = "plugins/BeaconE2E/delivery-reload-timed-b.yml"

    /** 兼容既有 hot_reload 用例（FR-171）沿用的两个路径名。 */
    const val SUCCESS_PATH = OK_PATH
    const val FAILURE_PATH = FAIL_PATH

    const val OBSERVATION_FILE = "e2e-delivery-hot-reload.log"

    /**
     * FR-278 计时观测的来源标记：外部驱动据此筛出「超时预算实测」行，
     * 并从明细里解析 elapsed / interrupted 两个字段。
     */
    const val TIMED_SOURCE = "RELOAD_TIMED"

    private const val INITIAL_CONTENT = "marker: original\n"
    private const val POLL_INTERVAL_TICKS = 20L
    private const val ALIVE_INTERVAL = 5L

    /** 睡眠时长：需明显超过 agent 侧 `delivery.config-reload-timeout-ms`（真机取 3000ms）。 */
    private const val HANG_MS = 60_000L

    /**
     * 计时路径的睡眠时长：同样须明显超过被测超时（本用例会下发 2000 / 12000）。
     *
     * 取 60s 而不是「刚好超一点」：睡满说明**超时没有生效**（未被中断），
     * 那本身就是一条要暴露的失败形态，不该被「睡够了就返回」掩盖。
     */
    private const val TIMED_SLEEP_MS = 60_000L

    private val WATCHED =
        listOf(OK_PATH, FAIL_PATH, THROW_PATH, ERROR_PATH, HANG_PATH, NULL_PATH, LEGACY_PATH, TIMED_A_PATH, TIMED_B_PATH)

    /**
     * 计时路径的「裸 dataId」别名（FR-278）。
     *
     * ## 为什么需要别名
     *
     * 变更集里的标识有两套词表，取决于哪条链路触发：
     *  - 交付 / 文件树链路：`plugins/BeaconE2E/xxx.yml`（相对服务器根的路径）；
     *  - **V1 配置中心链路**：控制面里配置项的 dataId（`xxx.yml`，不含目录）。
     * FR-278 只想验证「下发新超时值 → 运行时按新值工作」，走轻量的 V1 配置中心即可，
     * 不必造一张交付单。故这里为计时路径额外接受裸 dataId。
     *
     * 只对这两条新路径开别名，既有路径的匹配语义保持不变（否则会悄悄改变既有用例的口径）。
     */
    private val TIMED_DATA_IDS =
        mapOf(
            "delivery-reload-timed-a.yml" to TIMED_A_PATH,
            "delivery-reload-timed-b.yml" to TIMED_B_PATH,
        )

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
        val path = resolveWatched(changed) ?: return ConfigReloadResult.ok().withAppliedMd5(newMd5)
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
            // FR-278：计时路径——睡满并记录「实际被掐断的时刻」，即以毫秒量出生效中的超时预算。
            TIMED_A_PATH, TIMED_B_PATH -> {
                val (elapsed, interrupted) = sleepMeasuring(TIMED_SLEEP_MS)
                E2EObservation.append(
                    observation,
                    TIMED_SOURCE,
                    path,
                    md5,
                    "elapsed=${elapsed}ms interrupted=$interrupted",
                )
                ConfigReloadResult.ok().withAppliedMd5(newMd5)
            }
            else -> ConfigReloadResult.ok().withAppliedMd5(newMd5)
        }
    }

    /**
     * 从本次变更集里解析出**本探针要按哪条受控路径响应**；无命中返回 null。
     *
     * 先按完整路径匹配（交付 / 文件树链路的词表、也是既有用例的口径），
     * 再回落到计时路径的裸 dataId 别名（V1 配置中心链路）。
     * 排除 LEGACY_PATH / NULL_PATH：它们由各自的专用监听处理，不得影响上报式结果。
     */
    private fun resolveWatched(changed: Set<String>): String? {
        WATCHED.firstOrNull { it != LEGACY_PATH && it != NULL_PATH && changed.contains(it) }
            ?.let { return it }
        return TIMED_DATA_IDS.entries.firstOrNull { changed.contains(it.key) }?.value
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

    /**
     * 睡 [ms] 毫秒，并**量出实际睡了多久、是否被中断**（FR-278 运行时生效的直接证据）。
     *
     * 派发器判超时时会 `future.cancel(true)` 打断执行线程（见 `ReloadOutcomeCollector.onTimeout`），
     * 于是这里的 [Thread.sleep] 抛 InterruptedException —— 抛出的时刻即「超时预算到期的时刻」。
     * 因此 elapsed 就是把生效中的超时值**量出来**：这是运行时行为，不是读配置缓存。
     *
     * 与 [sleepQuietly] 刻意分开：那个服务于 HANG 分支，只需「卡住」、不需要测量，
     * 混在一起会让 HANG 的语义（睡满 60s 后正常返回）被测量逻辑改变。
     *
     * **不重新设置中断标志**：本线程（线程池中的 reload 线程）随后要回到池里继续服务，
     * 带着中断标志返回会让下一轮 reload 一进入就抛 InterruptedException（假超时）。
     * 中断在此处已消费完毕、语义已记入观测，故吞掉是正确处置。
     *
     * @return (实际耗时毫秒, 是否被中断)
     */
    private fun sleepMeasuring(ms: Long): Pair<Long, Boolean> {
        val startedAt = System.nanoTime()
        val interrupted =
            try {
                Thread.sleep(ms)
                false
            } catch (_: InterruptedException) {
                true
            }
        val elapsed = (System.nanoTime() - startedAt) / 1_000_000
        return elapsed to interrupted
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
