package top.wcpe.beacon.agent.core.settings

import top.wcpe.beacon.agent.core.api.EffectiveConfigView
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.pullAgentSettings
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.io.File
import java.util.concurrent.atomic.AtomicBoolean

/**
 * 下发配置的拉取与应用（FR-277 / FR-278 的闭环末段）。
 *
 * ## 它补的是哪一环
 *
 * 在它之前，链路是「Web 改 → 控制面存 → ✂ → agent 生效」——中间断着：
 * agent 只读本地缓存，永远不会去问控制面「有没有新配置」。
 * 本类就是那段缺失的连接。
 *
 * ## 三条设计要点
 *
 * 1. **失败绝不降级本地行为**：拉取失败时保留已有缓存继续工作。
 *    若失败就退回 config.yml 或代码默认，会出现「控制面一抖，全部 agent 回到出厂设置」——
 *    对重载超时这类参数，等于悄悄关掉保护。
 * 2. **写入前过一遍键级防护**：服务端已挡过一次禁区键，这里再挡一次。
 *    双保险的意义是：任一侧被绕过（例如服务端版本较旧、或有人直接改库），
 *    agent 仍不会因为缓存里出现 `beacon.endpoints` 就把自己指向别的控制面。
 * 3. **变更才应用**：与上次值相同则不触发行为更新、不打日志。
 *    周期拉取必然大量重复，每次都「应用」会刷日志，也会让热重载的噪声盖住真变更。
 *
 * @param apiClient 控制面客户端
 * @param store 下发缓存（落盘，断连时仍可用）
 * @param view 有效配置视图（热重载超时的落点）
 * @param adapter 日志出口
 */
internal class PushedConfigCoordinator(
    private val apiClient: BeaconApiClient,
    private val store: PushedConfigStore,
    private val view: EffectiveConfigView,
    private val adapter: PlatformAdapter,
) {
    /** 上次已应用的键值快照：用于判断「真的变了没有」。 */
    @Volatile
    private var applied: Map<String, String> = emptyMap()

    /** 防重入：一轮拉取未结束时不再开第二轮（周期任务可能重叠）。 */
    private val inFlight = AtomicBoolean(false)

    /** 调度代号：重注册/停机时推进，令旧代循环退出。 */
    private val generation = java.util.concurrent.atomic.AtomicInteger(0)

    /** 从缓存初始化已应用快照（进程重启后不该把同样的值再"应用"一遍并刷日志）。 */
    fun primeFromCache() {
        applied = store.load().values
    }

    /**
     * 拉一轮并应用。
     *
     * 返回值仅供测试与诊断：true 表示本轮
     * 拿到了配置（无论是否有变化），false 表示拉取失败。
     */
    fun pullAndApply(
        identity: AgentIdentity,
        timeoutMsKey: String = KEY_RELOAD_TIMEOUT_MS,
    ): Boolean {
        if (!inFlight.compareAndSet(false, true)) {
            return false
        }
        try {
            val snapshot = apiClient.pullAgentSettings(identity) ?: return false
            // 服务端剔除的键要让它可见：否则「Web 上改了却没生效」无从排查。
            if (snapshot.rejectedKeys.isNotEmpty()) {
                adapter.warn(
                    "控制面下发的配置含禁止项已忽略（接入目标与身份只能本地决定）：" +
                        snapshot.rejectedKeys.joinToString(", "),
                )
            }
            // 键级防护第二道：服务端应已剔除，这里再挡一次（见类注释第 2 点）。
            val (allowed, rejectedByAgent) = PushedConfigPolicy.partition(snapshot.values)
            if (rejectedByAgent.isNotEmpty()) {
                adapter.warn(
                    "本地又拦下禁止下发的配置项（服务端未剔除，可能是版本差异）：" +
                        rejectedByAgent.joinToString(", "),
                )
            }
            // 落盘：断连后仍能用这份值工作。
            store.update(allowed, snapshot.namespace, snapshot.serverId)
            applyIfChanged(allowed, timeoutMsKey)
            return true
        } finally {
            inFlight.set(false)
        }
    }

    /** 值真的变了才应用并记日志（周期拉取大量重复，每次应用会淹没真变更）。 */
    private fun applyIfChanged(
        values: Map<String, String>,
        timeoutMsKey: String,
    ) {
        if (values == applied) {
            return
        }
        applied = values
        // 重载超时有独立的运行时落点（视图里的派发器），必须显式推给它；
        // 其余参数在下一次 full reload 时随 readEffectiveSettings 生效。
        val rawTimeout = values[timeoutMsKey]
        if (rawTimeout != null) {
            val parsed = rawTimeout.trim().toLongOrNull()
            if (parsed == null) {
                adapter.warn("下发的 $timeoutMsKey 不是合法整数已忽略：$rawTimeout")
            } else {
                val sanitized = parsed.coerceIn(0L, MAX_RELOAD_TIMEOUT_MS)
                if (sanitized != parsed) {
                    adapter.warn("下发的 $timeoutMsKey=$parsed 超出合理范围，已收口为 $sanitized")
                }
                view.updateReloadTimeout(sanitized)
                adapter.info("已应用下发的重载超时：${sanitized}ms（下一轮配置变更即生效）")
            }
        }
        adapter.info("已接收控制面下发的 agent 配置（共 ${values.size} 项）")
    }

    /**
     * 启动周期拉取（自调度）。
     *
     * ## 为什么调度逻辑放在本类而不是装配层
     *
     * 装配层的核心函数已经贴着 detekt 的函数数与长度上限，再往里塞「建实例 + 注册回调 +
     * 定时重排」会把它压爆。而这些逻辑本来就属于「下发配置」这一件事，放在本类更内聚：
     * 调用方只需 `start(identity)` 一行。
     *
     * ## gen 的作用
     *
     * 重注册会启动新一代循环，旧代自行退出（与配置长轮询同款做法），
     * 避免两次注册后两条循环同时拉取、把日志刷成双份。
     *
     * @param runAsyncDelayed 平台提供的延迟执行（壳层注入，不在 MC 主线程执行）
     */
    fun start(
        identity: AgentIdentity,
        runAsyncDelayed: (Long, () -> Unit) -> Unit,
    ) {
        val gen = generation.incrementAndGet()
        schedule(identity, runAsyncDelayed, gen)
    }

    /** 停掉周期拉取（停机时调用）：推进代号让在途的下一轮自行退出。 */
    fun stop() {
        generation.incrementAndGet()
    }

    private fun schedule(
        identity: AgentIdentity,
        runAsyncDelayed: (Long, () -> Unit) -> Unit,
        gen: Int,
    ) {
        runAsyncDelayed(POLL_INTERVAL_MS) {
            // 代号不匹配说明已重注册或停机：本轮直接退出，不再续排。
            if (gen == generation.get()) {
                runCatching { pullAndApply(identity) }
                    .onFailure { adapter.warn("下发配置拉取异常（保持当前配置）：${it.message}") }
                schedule(identity, runAsyncDelayed, gen)
            }
        }
    }

    internal companion object {
        /**
         * 装配期构造：内部建缓存存储并用缓存预热「已应用」快照
         * （进程重启后不重复应用同样的值、不刷重复日志）。
         */
        fun forAssembly(
            apiClient: BeaconApiClient,
            dataFolder: File,
            codec: JsonCodec,
            view: EffectiveConfigView,
            adapter: PlatformAdapter,
        ): PushedConfigCoordinator =
            PushedConfigCoordinator(apiClient, PushedConfigStore(dataFolder.toPath(), codec), view, adapter)
                .also { it.primeFromCache() }

        /** 拉取周期（毫秒）：30 秒。变更本就低频，改小收益有限，改大会拉长生效延迟。 */
        const val POLL_INTERVAL_MS = 30_000L

        /** 重载超时的键名（与 config.yml 一致）。 */
        const val KEY_RELOAD_TIMEOUT_MS = "delivery.config-reload-timeout-ms"

        /**
         * 重载超时的合理上限：1 小时。
         *
         * 超过这个值的下发多半是手误（例如把秒当毫秒填）。收口而不是接受，
         * 是因为一个过大的超时会让「插件卡死」退化成「永远等不到结论」——
         * 那正是本功能要消灭的形态。
         */
        const val MAX_RELOAD_TIMEOUT_MS = 3_600_000L
    }
}
