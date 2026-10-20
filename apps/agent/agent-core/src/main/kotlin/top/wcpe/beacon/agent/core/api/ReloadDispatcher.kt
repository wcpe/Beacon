package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.ConfigChangeListener
import top.wcpe.beacon.agent.api.ReportingConfigChangeListener
import java.util.concurrent.ExecutionException
import java.util.concurrent.ExecutorService
import java.util.concurrent.Future
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.ThreadFactory
import java.util.concurrent.ThreadPoolExecutor
import java.util.concurrent.TimeUnit
import java.util.concurrent.TimeoutException
import java.util.concurrent.atomic.AtomicInteger

/**
 * 单个监听器的隔离执行器：把「调用插件」这件危险事收进一个可超时、可兜错的黑盒。
 *
 * 拆成独立类而非留在 [EffectiveConfigView] 里，是因为视图本职是「读配置 + 注册监听」，
 * 并发与超时是另一件事；混在一起既让视图函数数持续膨胀（detekt TooManyFunctions），
 * 也让「派发策略」和「配置存储」无法各自演进。
 *
 * @param reloadTimeoutMs 单监听器超时（毫秒）；**0 表示不设超时、就地内联执行**
 *                        （默认值，保持既有调用点与测试不变）。
 *                        该值须小于控制面的 `activate_timeout_sec`——否则控制面先判整体超时，
 *                        回执里就看不出是哪个插件卡住。
 * @param executor        执行监听器的线程池；传 null 且超时 > 0 时由本类自建守护线程池
 *                        （不让卡死插件拖住 JVM 退出）。
 */
internal class ReloadDispatcher(
    private val reloadTimeoutMs: Long = 0,
    executor: ExecutorService? = null,
) {
    /** 仅在启用超时时才需要线程池；未启用超时的内联路径零线程开销。 */
    private val executor: ExecutorService? =
        when {
            reloadTimeoutMs <= 0L -> null
            executor != null -> executor
            else -> newDaemonPool()
        }

    /**
     * 隔离执行一个监听器，任何失败都折算成该监听器的失败结果。
     *
     * 三条失败路径必须都落到同一形状上：
     * 1. **提交被拒**（池已关闭 / 队列满）——按该监听器失败计，不得让整个派发抛出去；
     * 2. **执行超时**——按失败计并 cancel，指明是哪个插件；
     * 3. **执行抛 Throwable**——插件侧 `NoClassDefFoundError` / `LinkageError` 在
     *    `agent-api` 版本不匹配时很常见，只兜 `Exception` 会让异常逃逸并打死派发线程。
     */
    fun dispatch(
        listener: ConfigChangeListener,
        changedDataIds: Set<String>,
        newMd5: String,
    ): PluginReloadOutcome {
        val name = listener.javaClass.name
        val pool = executor ?: return invoke(listener, changedDataIds, newMd5)
        val submitted =
            runCatching { pool.submit<PluginReloadOutcome> { invoke(listener, changedDataIds, newMd5) } }
        return submitted.fold(
            onSuccess = { future -> awaitOutcome(name, future) },
            onFailure = { e -> failed(name, e) },
        )
    }

    /** 等待已提交任务的结果；超时/异常/中断均折算为该监听器的失败结果。 */
    private fun awaitOutcome(
        name: String,
        future: Future<PluginReloadOutcome>,
    ): PluginReloadOutcome =
        try {
            future.get(reloadTimeoutMs, TimeUnit.MILLISECONDS)
        } catch (e: TimeoutException) {
            future.cancel(true)
            PluginReloadOutcome(listener = name, ok = false, reason = "重载超时（${reloadTimeoutMs}ms）", timeout = true)
        } catch (e: ExecutionException) {
            // invoke 内部已兜住 Throwable，正常不会走到这里；保留兜底以免线程池实现差异。
            failed(name, e.cause ?: e)
        } catch (e: InterruptedException) {
            future.cancel(true)
            // 中断标志必须恢复：吞掉它会让上层线程池误以为没被中断。
            Thread.currentThread().interrupt()
            PluginReloadOutcome.failed(name, "派发线程被中断，重载结果未知")
        } catch (e: Throwable) {
            future.cancel(true)
            failed(name, e)
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
}
