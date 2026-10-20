package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.ConfigChangeListener
import top.wcpe.beacon.agent.api.DataIdPrefix
import top.wcpe.beacon.agent.api.EffectiveConfig
import top.wcpe.beacon.agent.api.ListenerHandle
import top.wcpe.beacon.agent.api.ReportingConfigChangeListener
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import java.util.Optional
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.ExecutionException
import java.util.concurrent.ExecutorService
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.ThreadFactory
import java.util.concurrent.ThreadPoolExecutor
import java.util.concurrent.TimeUnit
import java.util.concurrent.TimeoutException
import java.util.concurrent.atomic.AtomicInteger

/**
 * EffectiveConfig 的 core 实现：读取来自 EffectiveConfigStore，变更监听由 apply 流程经
 * [fireChanged] 派发。两个平台壳共用，避免重复。
 *
 * 监听注册支持**按 dataId 路径前缀过滤**（`onChange(prefix, listener)`）：插件只订阅自己目录下的
 * 配置，别人的变更不会把它叫醒。匹配规则见 [DataIdPrefix]。
 *
 * @param store           有效配置存储
 * @param reloadTimeoutMs 单个监听器的重载超时（毫秒）。**0 表示不设超时、就地内联执行**
 *                        （默认值，保持既有调用点与测试不变）；生产由壳层按
 *                        `AgentSettings.configReloadTimeoutMs` 注入。
 *                        该值必须小于控制面的 `activate_timeout_sec`——否则控制面先判整体超时，
 *                        回执里就看不出是哪个插件卡住。
 * @param reloadExecutor  执行监听器的线程池；传 null 且 [reloadTimeoutMs] > 0 时由本类自建
 *                        守护线程池（不让卡死插件拖住 JVM 退出）。
 */
class EffectiveConfigView(
    private val store: EffectiveConfigStore,
    private val reloadTimeoutMs: Long = 0,
    reloadExecutor: ExecutorService? = null,
) : EffectiveConfig {
    // 监听器表：以句柄对象自身为键，便于注销。值里带前缀，注销时只认键。
    private val listeners = ConcurrentHashMap<Any, Registration>()

    /** 仅在启用超时时才需要线程池；未启用超时的内联路径零线程开销。 */
    private val executor: ExecutorService? =
        when {
            reloadTimeoutMs <= 0L -> null
            reloadExecutor != null -> reloadExecutor
            else -> newDaemonPool()
        }

    override fun dataIds(): List<String> = store.dataIds()

    override fun raw(dataId: String): Optional<String> = Optional.ofNullable(store.item(dataId)?.content)

    override fun format(dataId: String): Optional<String> = Optional.ofNullable(store.item(dataId)?.format)

    override fun md5(dataId: String): Optional<String> = Optional.ofNullable(store.item(dataId)?.md5)

    override fun onChange(listener: ConfigChangeListener): ListenerHandle = onChange(null, listener)

    /** 统一注册入口：前缀为 null/空表示不过滤（老语义），故单参重载委托到本方法。 */
    override fun onChange(
        pathPrefix: String?,
        listener: ConfigChangeListener,
    ): ListenerHandle {
        val key = Any()
        listeners[key] = Registration(pathPrefix, listener)
        return ListenerHandle { listeners.remove(key) }
    }

    /**
     * 由 apply 流程在有效配置变更后调用，逐监听器隔离派发并返回聚合结果。
     *
     * 五条刻意设计（见 `docs/specs/delivery-reload-ack.md` §3.2）：
     * 1. **逐监听器独立兜错**：一个插件抛异常不得让后续插件收不到通知。原实现是裸 `for`
     *    循环，多插件共存时先抛者会让其余插件静默停留在旧配置。
     * 2. **catch `Throwable` 而非 `Exception`**：插件侧 `NoClassDefFoundError` / `LinkageError`
     *    在 `agent-api` 版本不匹配时很常见，只兜 `Exception` 会让异常逃逸并打死调用线程
     *    （交付命令执行线程），连回执都发不出去。
     * 3. **消费上报结果**：实现 [ReportingConfigChangeListener] 的插件，其返回值（成功/失败 +
     *    原因 + 实际加载的 md5/版本/条数）进入聚合报告，交付路径据此做一票否决并核对版本。
     * 4. **逐监听器超时**：单个插件卡死不得拖住整批；超时按失败计并指明是哪个插件。
     * 5. **前缀过滤发生在执行之前**：带前缀注册的监听器只在「本次变更里有自己关心的路径」时
     *    才被执行；不相关就**既不回调、也不进 outcomes**——它没跑过，不该凭空多出一条成功记录，
     *    更不该因为「没跑」而被判失败。
     */
    fun fireChanged(
        changedDataIds: Set<String>,
        newMd5: String,
    ): ConfigReloadReport {
        val snapshot = listeners.values.toList()
        val outcomes = ArrayList<PluginReloadOutcome>(snapshot.size)
        // 快照迭代，避免监听器在回调内注销自身导致 ConcurrentHashMap 弱一致迭代漏派发。
        for (registration in snapshot) {
            // null 表示本次与它无关：直接跳过（过滤必须在调用之前，否则照样叫醒了插件）。
            val relevant = registration.relevantSubsetOf(changedDataIds) ?: continue
            outcomes += runIsolated(registration.listener, relevant, newMd5)
        }
        return ConfigReloadReport(changedDataIds, newMd5, outcomes)
    }

    /** 单个监听器的隔离执行：无执行器时内联，有执行器时带超时。 */
    private fun runIsolated(
        listener: ConfigChangeListener,
        changedDataIds: Set<String>,
        newMd5: String,
    ): PluginReloadOutcome {
        val name = listener.javaClass.name
        val pool = executor ?: return invoke(listener, changedDataIds, newMd5)

        val future =
            try {
                pool.submit<PluginReloadOutcome> { invoke(listener, changedDataIds, newMd5) }
            } catch (e: Throwable) {
                // 线程池已关闭 / 队列拒绝：不得让派发整体失败，按单监听器失败计入。
                return failed(name, e)
            }
        return try {
            future.get(reloadTimeoutMs, TimeUnit.MILLISECONDS)
        } catch (e: TimeoutException) {
            future.cancel(true)
            PluginReloadOutcome(
                listener = name,
                ok = false,
                reason = "重载超时（${reloadTimeoutMs}ms）",
                timeout = true,
            )
        } catch (e: ExecutionException) {
            // invoke 内部已兜住 Throwable，正常不会走到这里；保留兜底以免线程池实现差异。
            failed(name, e.cause ?: e)
        } catch (e: InterruptedException) {
            future.cancel(true)
            Thread.currentThread().interrupt()
            PluginReloadOutcome.failed(name, "派发线程被中断，重载结果未知")
        } catch (e: Throwable) {
            future.cancel(true)
            failed(name, e)
        }
    }

    /** 真正调用监听器，并把上报结果映射为结构化结果。 */
    private fun invoke(
        listener: ConfigChangeListener,
        changedDataIds: Set<String>,
        newMd5: String,
    ): PluginReloadOutcome {
        val name = listener.javaClass.name
        return try {
            if (listener is ReportingConfigChangeListener) {
                // 缺结果不得被当作成功：返回 null 按失败处置（护栏「不得假报成功」）。
                val result =
                    listener.onConfigChangedReporting(changedDataIds, newMd5)
                        ?: return PluginReloadOutcome.failed(name, "监听器未返回重载结果（null）")
                PluginReloadOutcome(
                    listener = name,
                    ok = result.isOk,
                    reason = result.reason(),
                    detailJson = result.detailJson(),
                    appliedMd5 = result.appliedMd5().orElse(""),
                    appliedVersion = result.appliedVersion().orElse(""),
                    appliedCount = if (result.appliedCount().isPresent) result.appliedCount().asInt else null,
                )
            } else {
                listener.onConfigChanged(changedDataIds, newMd5)
                PluginReloadOutcome.ok(name)
            }
        } catch (e: Throwable) {
            failed(name, e)
        }
    }

    /** 原因只带异常类型与 message：避免把插件内部堆栈细节铺到回执里。 */
    private fun failed(
        name: String,
        e: Throwable,
    ): PluginReloadOutcome = PluginReloadOutcome.failed(name, "${e.javaClass.simpleName}: ${e.message ?: ""}")

    private companion object {
        /** 并发重载的插件数上限；超出者排队（超时仍按各自独立计时，因计时从 get 开始）。 */
        const val POOL_SIZE = 4

        /**
         * 自建守护线程池：卡死的插件线程不会阻止 JVM 退出；核心线程可超时回收。
         * 容量取小值——卡死线程本就无法强杀，池子过大只是放大泄漏面。
         *
         * 放在伴生对象而非实例方法：它是纯粹的静态工厂（不碰本类状态），
         * 也避免本类实例方法数无谓增长。
         */
        fun newDaemonPool(): ExecutorService {
            val seq = AtomicInteger(0)
            val factory =
                ThreadFactory { r ->
                    Thread(r, "beacon-config-reload-${seq.incrementAndGet()}").apply { isDaemon = true }
                }
            return ThreadPoolExecutor(
                POOL_SIZE,
                POOL_SIZE,
                60L,
                TimeUnit.SECONDS,
                LinkedBlockingQueue(),
                factory,
            ).apply { allowCoreThreadTimeOut(true) }
        }
    }

    /**
     * 一条监听注册。
     *
     * @param pathPrefix 过滤前缀；null/空表示不过滤（老语义，任何变更都回调）
     * @param listener   下游监听器
     */
    private class Registration(
        private val pathPrefix: String?,
        val listener: ConfigChangeListener,
    ) {
        /**
         * 取本次变更中该监听器该看到的子集；返回 null 表示本次与它无关、**不该调用**。
         *
         * 三条语义：
         * 1. 无前缀 → 原样返回（含空集合也照常回调，保持既有行为不变）。
         * 2. 有前缀且零匹配 → null，跳过（这才是「不被无关唤醒」）。
         * 3. 有前缀且部分匹配 → 只给匹配项，插件无需再自行筛选，且不会顺手读到别人的文件。
         */
        fun relevantSubsetOf(changedDataIds: Set<String>): Set<String>? {
            if (!DataIdPrefix.isFiltering(pathPrefix)) {
                return changedDataIds
            }
            val matched = DataIdPrefix.filter(pathPrefix, changedDataIds)
            return matched.ifEmpty { null }
        }
    }
}
