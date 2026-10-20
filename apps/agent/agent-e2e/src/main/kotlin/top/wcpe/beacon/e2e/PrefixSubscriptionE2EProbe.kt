package top.wcpe.beacon.e2e

import taboolib.common.platform.function.getDataFolder
import taboolib.common.platform.function.info
import taboolib.common.platform.function.submit
import top.wcpe.beacon.agent.api.BeaconAgentProvider
import top.wcpe.beacon.agent.api.ListenerHandle
import java.io.File

/**
 * `EffectiveConfig.onChange(prefix, listener)` 路径前缀过滤订阅的端到端探针（Bukkit 壳）。
 *
 * ## 为什么必须单独验证
 *
 * 无前缀的 `onChange(listener)` 是老语义：任何 dataId 变更都回调，插件自己筛。
 * 带前缀的新语义则要求「**只唤醒关心的插件**」——不相关时**既不回调、也不进 outcomes**。
 * 这两者的差异无法用单 dataId 的场景证明：只有一个 dataId 时，通配与过滤的表现完全一样。
 * 故本探针注册**多个不同前缀的监听器**，然后由外部让**多个 dataId 同一轮变更**，
 * 按「哪个监听器被唤醒」反推过滤是否真的生效。
 *
 * ## 与 agent 内部数据的对应关系
 *
 * 前缀匹配是纯 `startsWith` 字符串匹配（`DataIdPrefix`），不做目录边界推断。
 * 这一点很反直觉：前缀 `a/b.yml` 也会匹配到 `a/b.yml.bak`。
 * 故观测里刻意包含一个「看似同前缀但实际更长的 dataId」，把这条语义钉在真机证据上。
 *
 * ## 观测格式
 *
 * 写到 `e2e-prefix-subscription.log`，第 2 段取值即行语义：
 *  - `PREFIX_REGISTERED`  已注册某前缀的监听器（第 3 段 = 前缀，`-` 表示无前缀）
 *  - `PREFIX_CALLBACK`    某前缀的监听器被回调（第 3 段 = 前缀，第 5 段 = 实际收到的 changedDataIds）
 *  - `PREFIX_OUTCOME`     兜底：注册失败/异常
 *
 * 外部断言的核心是：**变更是 `X` 前缀时，只有 `X` 前缀的监听器出现 PREFIX_CALLBACK 行**，
 * 其余前缀一行都不该有。
 *
 * 由环境变量 `BEACON_E2E_PREFIX_SUB` 门控（非空才启用）；
 * 监听的前缀清单由 `BEACON_E2E_PREFIX_LIST` 给出（逗号分隔），默认 `beacon-e2e` 与 `beacon-other`。
 */
object PrefixSubscriptionE2EProbe {
    /** 标记文件名：外部驱动据此断言。 */
    private const val OBSERVATION_FILE = "e2e-prefix-subscription.log"

    /** 轮询周期（tick，20 tick/秒）：约每 4 秒补注册一次（agent 可能晚于插件就绪）。 */
    private const val POLL_INTERVAL_TICKS = 80L

    /**
     * 要注册的前缀清单。
     *
     * 默认两套：`beacon-e2e`（控制面侧会下发的 dataId 前缀）与 `beacon-other`（**不应**被下发的对照前缀）。
     * 对照组是本探针的关键：只有「一个该命中、一个不该命中」同时存在，
     * 才能证明过滤生效，而不是恰好只有一个监听器。
     */
    private val prefixes: List<String> =
        (System.getenv("BEACON_E2E_PREFIX_LIST") ?: "beacon-e2e,beacon-other")
            .split(',')
            .map { it.trim() }
            .filter { it.isNotEmpty() }

    /** 每个前缀对应的注销句柄（前缀 → 句柄），DISABLE 时统一释放。 */
    private val handles = mutableMapOf<String, ListenerHandle>()

    /** 启动探针：清旧标记 → 注册各前缀监听器 → 启异步补注册。 */
    fun start() {
        if (System.getenv("BEACON_E2E_PREFIX_SUB").isNullOrBlank()) {
            info("未配置 BEACON_E2E_PREFIX_SUB，跳过前缀订阅探针")
            return
        }
        val markFile = File(getDataFolder(), OBSERVATION_FILE)
        // 清空上轮残留，保证每次 run 的标记文件只含本轮观测。
        if (markFile.exists()) {
            markFile.delete()
        }
        info("Beacon E2E 前缀订阅探针已启用，关注前缀=$prefixes，标记文件=${markFile.absolutePath}")
        registerAll(markFile)
        // agent 可能在插件 ENABLE 时尚未就绪，持续补注册直到全部成功。
        submit(async = true, delay = POLL_INTERVAL_TICKS, period = POLL_INTERVAL_TICKS) {
            registerAll(markFile)
        }
    }

    /** 停止探针：释放全部监听器（由 BeaconE2EBukkit 在 DISABLE 阶段调用）。 */
    fun stop() {
        handles.values.forEach { it.remove() }
        handles.clear()
    }

    /**
     * 为尚未注册成功的前缀注册监听器。
     *
     * 回调里**把收到的 changedDataIds 原样落盘**：外部据此不止能断言「谁被唤醒」，
     * 还能断言「被唤醒者看到的是裁剪后的子集而非全量变更」——这正是带前缀订阅的第二个语义
     * （插件无需再自行筛选，也不会顺手读到别人的配置）。
     */
    private fun registerAll(markFile: File) {
        if (!BeaconAgentProvider.isAvailable()) {
            return
        }
        for (prefix in prefixes) {
            if (handles.containsKey(prefix)) {
                continue
            }
            runCatching {
                BeaconAgentProvider.get().config().onChange(prefix) { changedDataIds, newMd5 ->
                    E2EObservation.append(
                        markFile,
                        "PREFIX_CALLBACK",
                        prefix,
                        newMd5,
                        "changed=${changedDataIds.sorted().joinToString(",").ifEmpty { "EMPTY" }}",
                    )
                }
            }.onSuccess { handle ->
                handles[prefix] = handle
                E2EObservation.append(markFile, "PREFIX_REGISTERED", prefix, "-", "已注册前缀监听器")
            }.onFailure { e ->
                E2EObservation.append(
                    markFile,
                    "PREFIX_OUTCOME",
                    prefix,
                    "-",
                    "注册失败=${e.javaClass.simpleName}: ${e.message}",
                )
            }
        }
    }
}
