package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.ConfigChangeListener
import top.wcpe.beacon.agent.api.ReportingConfigChangeListener
import top.wcpe.beacon.agent.core.log.LogRedactor
import java.util.concurrent.ArrayBlockingQueue
import java.util.concurrent.BlockingQueue
import java.util.concurrent.ExecutorService
import java.util.concurrent.RejectedExecutionException
import java.util.concurrent.RejectedExecutionHandler
import java.util.concurrent.ThreadFactory
import java.util.concurrent.ThreadPoolExecutor
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicLong

/**
 * 单个监听器的隔离执行器：把「调用插件」这件危险事收进一个可超时、可兜错的黑盒。
 *
 * 拆成独立类而非留在 [EffectiveConfigView] 里，是因为视图本职是「读配置 + 注册监听」，
 * 并发与超时是另一件事；混在一起既让视图函数数持续膨胀（detekt TooManyFunctions），
 * 也让「派发策略」和「配置存储」无法各自演进。
 *
 * 本类只负责**提交与调用**；「等结果并把各种失败折算成结果」在 [ReloadOutcomeCollector]，
 * 两类变化的理由不同（池容量 vs 超时语义），分开才能各自独立演进。
 *
 * @param reloadTimeoutMs 单个监听器的超时（毫秒）；**0 表示不设超时、就地内联执行**
 *                        （默认值，保持既有调用点与测试不变）。
 *                        该值须小于控制面的 `activate_timeout_sec`——否则控制面先判整体超时，
 *                        回执里就看不出是哪个插件卡住。
 * @param executor        执行监听器的线程池；传 null 且超时 > 0 时由本类自建守护线程池
 *                        （不让卡死插件拖住 JVM 退出）。
 */
internal class ReloadDispatcher(
    reloadTimeoutMs: Long = 0,
    executor: ExecutorService? = null,
    /** 并发位数量（FR-280）：由 AgentSettings.configReloadPoolSize 注入，收口到 [1, 256]。 */
    private val poolSize: Int = DEFAULT_POOL_SIZE,
) {
    /**
     * 超时预算：**可变**，因为控制面可在运行期下发新值（FR-278 热重载）。
     *
     * 用 AtomicLong 而不是普通字段：派发线程读它、配置应用线程写它，
     * 必须让新值对下一轮派发立即可见（普通 Long 在 JVM 上不保证可见性）。
     */
    private val timeoutMs = AtomicLong(reloadTimeoutMs)

    /** 外部注入的池（生命周期归注入方）；null 表示由本类按需自建。 */
    private val injectedPool: ExecutorService? = executor

    @Volatile
    private var selfBuiltPool: ExecutorService? = null

    /**
     * 是否已关闭。
     *
     * 关闭后**不再自建新池**：否则 `currentExecutor()` 会热重载式地重新建一个，
     * 让「已关闭」变成「关掉旧的、悄悄开了新的」——关闭语义必须单向，
     * 否则停机后派发会继续执行，而调用方以为它已经停了。
     */
    @Volatile
    private var closed = false

    /**
     * 当前执行用的线程池；null 表示「未启用超时 → 内联执行」。
     *
     * 惰性创建：初始与热重载后都走同一条路径，避免两处初始化逻辑分叉。
     */
    private fun currentExecutor(): ExecutorService? = injectedPool ?: selfBuiltOrNull()

    /**
     * 未注入时按需自建；未启用超时（<=0）或已关闭返回 null。
     *
     * 返回 null 表示「内联执行」——但注意关闭后内联执行也是不希望的，
     * 故 [dispatchAll] 在关闭时直接报「未执行」而不是退回内联（见该函数）。
     */
    private fun selfBuiltOrNull(): ExecutorService? {
        if (closed || timeoutMs.get() <= 0L) {
            return null
        }
        return selfBuiltPool ?: synchronized(this) {
            if (closed) null else selfBuiltPool ?: newDaemonPool(poolSize).also { selfBuiltPool = it }
        }
    }

    /**
     * 释放本类**自建**的线程池。
     *
     * 只关自建的：外部注入的池由注入方负责生命周期，关掉别人的池属于越权。
     * 不关的后果是 agent 每次 reload 都留下一组线程——`allowCoreThreadTimeOut` 只是让它们
     * 空闲 60s 后回收，但若 reload 恰在满负荷时发生，新池与旧池会短暂叠加。
     *
     * 用 `shutdownNow` 而非 `shutdown`：已排队的任务没有理由继续执行——它们的目标配置
     * 已随本次 reload 过期了，跑完只会把旧配置写进新进程。
     */
    fun close() {
        closed = true
        selfBuiltPool?.shutdownNow()
        selfBuiltPool = null
    }

    /**
     * 热重载超时预算（FR-278）：下一次派发即按新值执行，无需重启。
     *
     * **不重建线程池**：超时值只影响 future.get 的等待时长，与池容量无关。
     * 重建池会让在途任务被打断，属于不必要的破坏。
     */
    fun updateTimeoutMs(newTimeoutMs: Long) {
        timeoutMs.set(newTimeoutMs.coerceAtLeast(0L))
    }

    /** 派发单个监听器（内部走批量路径，保证语义只有一处实现）。 */
    fun dispatch(
        listener: ConfigChangeListener,
        changedDataIds: Set<String>,
        newMd5: String,
    ): PluginReloadOutcome = dispatchAll(listOf(ReloadRequest(listener, changedDataIds, newMd5))).single()

    /**
     * **并发**派发一批监听器：全部提交后统一收结果。
     *
     * ## 为什么必须「先全提交、再统一收」
     *
     * 逐个 `dispatch`（提交一个 → 等到结果 → 再提交下一个）在池里永远只有 1 个任务在跑，
     * 总耗时是**各插件之和**。批量提交后统一收集，总耗时才是**最慢的那个**。
     * 单服几十个插件时这个差别是数量级的，也是「服务器多了会卡」的直接来源。
     *
     * @return 与入参**同序**的结果列表：顺序稳定，报告才可复现、测试才可断言。
     */
    fun dispatchAll(requests: List<ReloadRequest>): List<PluginReloadOutcome> {
        if (requests.isEmpty()) {
            return emptyList()
        }
        // 已关闭时一律报「未执行」：不能退回内联执行，否则停机后派发仍在跑插件逻辑，
        // 而调用方以为已经停了（关闭语义必须单向、可见）。
        if (closed) {
            val closedReason = RejectedExecutionException("执行器已关闭")
            // 复用收口器的判定（它会区分「饱和拒绝」与「不可用」，文案不同）。
            return ReloadOutcomeCollector(timeoutMs.get())
                .collectAll(
                    requests.map { req ->
                        ReloadOutcomeCollector.Submitted(
                            req.listener.javaClass.name,
                            Result.failure(closedReason),
                            AtomicBoolean(false),
                        )
                    },
                )
        }
        // 内联路径（未启用超时）没有并发载体，只能逐条执行——这是刻意保留的兼容行为。
        val pool = currentExecutor() ?: return requests.map { invoke(it.listener, it.changedDataIds, it.newMd5) }
        // 先**全部提交**（绝不中途等待），再交给收口器统一收。
        // collector 每轮新建：超时值可能刚被热重载改过，复用旧实例会沿用旧预算。
        return ReloadOutcomeCollector(timeoutMs.get()).collectAll(requests.map { submitOne(pool, it) })
    }

    /** 提交一条请求；提交失败（池满/已关闭）不抛，留到收口阶段折算成「未执行」。 */
    private fun submitOne(
        pool: ExecutorService,
        request: ReloadRequest,
    ): ReloadOutcomeCollector.Submitted {
        val started = AtomicBoolean(false)
        val future =
            runCatching {
                pool.submit<PluginReloadOutcome> {
                    started.set(true)
                    invoke(request.listener, request.changedDataIds, request.newMd5)
                }
            }
        return ReloadOutcomeCollector.Submitted(request.listener.javaClass.name, future, started)
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
                    // 插件自报的 reason / detailJson 是不可信输入，进回执前统一脱敏 + 限长：
                    // 回执会落控制面 DB 并直接渲染到前端，裸奔的明文凭据等于把插件密钥抄进审计表。
                    reason = sanitize(result.reason(), MAX_REASON_CHARS),
                    detailJson = sanitize(result.detailJson(), MAX_DETAIL_CHARS),
                    appliedMd5 = result.appliedMd5().orElse(""),
                    appliedVersion = result.appliedVersion().orElse(""),
                    appliedCount = if (result.appliedCount().isPresent) result.appliedCount().asInt else null,
                )
            } else {
                listener.onConfigChanged(changedDataIds, newMd5)
                PluginReloadOutcome.ok(name)
            }
        } catch (e: Throwable) {
            // 插件侧 NoClassDefFoundError / LinkageError 在 agent-api 版本不匹配时很常见，
            // 只兜 Exception 会让异常逃逸并打死派发线程。
            PluginReloadOutcome.failed(name, sanitize("${e.javaClass.simpleName}: ${e.message ?: ""}", MAX_REASON_CHARS))
        }
    }

    /**
     * 把不可信文本处理成可安全进回执的形态：先脱敏再限长。
     *
     * 顺序不可颠倒——先截断后脱敏可能把 `token=xxxx` 截成 `token=xx`，脱敏规则匹配不到，
     * 反而漏出半截密钥。
     */
    private fun sanitize(
        text: String,
        maxChars: Int,
    ): String {
        if (text.isEmpty()) {
            return text
        }
        val redacted = LogRedactor.redact(text)
        if (redacted.length <= maxChars) {
            return redacted
        }
        return redacted.take(maxChars) + TRUNCATED_MARK
    }

    /** 一条待办的重载请求（批量派发的入参）。 */
    data class ReloadRequest(
        val listener: ConfigChangeListener,
        val changedDataIds: Set<String>,
        val newMd5: String,
    )

    internal companion object {
        /**
         * 并发重载的插件数上限。
         *
         * 这里排的是**同一个 MC 进程内的业务插件**，不是「服务器台数」——60 台服务器各跑
         * 独立 agent 进程，池互不共享。
         *
         * 取 64 的理由：[dispatchAll] 是**真并发**，并发位小于插件数时多出来的会在队列里排队，
         * 总耗时退化成「批次 × 最慢插件」。单服装 60+ 插件是常见形态（大厅服尤其如此），
         * 取一个能覆盖单服插件规模的值，让常见场景一次跑完。
         *
         * 仍然有界（不是无界/不设限）：卡死不响应中断的插件会永久占住线程（无法强杀），
         * 无界会让「泄漏」无声累积；有界 + [REJECT_POLICY] 才能把饱和如实报出来。
         */
        const val DEFAULT_POOL_SIZE = 64

        /**
         * 饱和策略：拒绝而非排队。
         *
         * 不用 CallerRunsPolicy：那会让提交方（交付命令线程 / 配置应用线程）亲自跑插件逻辑，
         * 等于把「单插件卡死」升级成「派发线程卡死」，比拒绝更糟。
         */
        val REJECT_POLICY: RejectedExecutionHandler = ThreadPoolExecutor.AbortPolicy()

        /** 失败原因的长度上限（字符）：回执要落库并展示，不能让插件把 DB 列撑爆。 */
        const val MAX_REASON_CHARS = 512

        /**
         * 插件自定义 JSON 明细的长度上限（字符）。
         *
         * 取 4K 是因为它纯粹用于「给人看的附加信息」，真正需要核对的字段（md5/版本/条数）
         * 都在结构化字段里；明细超长说明插件在往这里塞不该塞的东西。
         */
        const val MAX_DETAIL_CHARS = 4096

        /** 截断标记：让读者一眼看出内容被裁剪过，不会误以为原文就这么短。 */
        const val TRUNCATED_MARK = "…（已截断）"

        /**
         * 自建守护线程池：卡死的插件线程不会阻止 JVM 退出；核心线程可超时回收。
         *
         * 队列必须有界（见 [QUEUE_CAPACITY] 与 [REJECT_POLICY] 的说明）：无界队列会把
         * 「没轮到执行」伪装成「执行超时」。
         */
        fun newDaemonPool(size: Int): ExecutorService = newPool(size, ArrayBlockingQueue(size), defaultThreadFactory(), REJECT_POLICY)

        /** 统一构造形态：守护线程 + 允许核心线程超时回收（不给 agent reload 留常驻线程）。 */
        fun newPool(
            size: Int,
            queue: BlockingQueue<Runnable>,
            factory: ThreadFactory,
            handler: RejectedExecutionHandler,
        ): ThreadPoolExecutor =
            ThreadPoolExecutor(
                size,
                size,
                60L,
                TimeUnit.SECONDS,
                queue,
                factory,
                handler,
            ).apply { allowCoreThreadTimeOut(true) }

        /** 守护线程工厂：线程名带序号，便于运维在 jstack 里认出是配置重载线程。 */
        fun defaultThreadFactory(): ThreadFactory {
            val seq = AtomicInteger(0)
            return ThreadFactory { r ->
                Thread(r, "beacon-config-reload-${seq.incrementAndGet()}").apply { isDaemon = true }
            }
        }
    }
}
