package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.ConfigChangeListener
import top.wcpe.beacon.agent.api.ReportingConfigChangeListener
import top.wcpe.beacon.agent.core.log.LogRedactor
import java.util.concurrent.ArrayBlockingQueue
import java.util.concurrent.BlockingQueue
import java.util.concurrent.ExecutionException
import java.util.concurrent.ExecutorService
import java.util.concurrent.Future
import java.util.concurrent.RejectedExecutionException
import java.util.concurrent.RejectedExecutionHandler
import java.util.concurrent.ThreadFactory
import java.util.concurrent.ThreadPoolExecutor
import java.util.concurrent.TimeUnit
import java.util.concurrent.TimeoutException
import java.util.concurrent.atomic.AtomicBoolean
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
    /** 自建的线程池（外部注入时为 null）；[close] 只关它。 */
    private val selfBuiltPool: ExecutorService? =
        if (reloadTimeoutMs > 0L && executor == null) newDaemonPool() else null

    /** 实际执行用的线程池；仅在启用超时时才需要，未启用超时的内联路径零线程开销。 */
    private val executor: ExecutorService? = executor ?: selfBuiltPool

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
        selfBuiltPool?.shutdownNow()
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
        // 任务体开头置位：区分「真的开始执行过」与「一直排在队列里没轮到」。
        val started = AtomicBoolean(false)
        val submitted =
            runCatching {
                pool.submit<PluginReloadOutcome> {
                    started.set(true)
                    invoke(listener, changedDataIds, newMd5)
                }
            }
        return submitted.fold(
            onSuccess = { future -> awaitOutcome(name, future, started) },
            onFailure = { e -> rejected(name, e) },
        )
    }

    /**
     * 提交阶段就被拒（池已饱和 / 已关闭）的收口。
     *
     * **必须与「超时」区分开**：超时意味着「跑过但没跑完」，运维该去查那个插件为什么慢；
     * 而这里根本**没被执行过**，运维要做的是降低并发或排查已卡死的线程。
     * 把两者混成一句「重载超时」会让排查方向完全错掉——这正是护栏「不得假报成功」的同源要求：
     * 不要说一件没发生的事。
     */
    private fun rejected(
        name: String,
        e: Throwable,
    ): PluginReloadOutcome {
        val saturated = e is RejectedExecutionException
        val reason =
            if (saturated) {
                "重载执行器饱和（$POOL_SIZE 个并发位与 $QUEUE_CAPACITY 个排队位已满，本次未执行）：" +
                    "请降低单次变更的插件数，或排查已卡死未响应中断的插件"
            } else {
                // 非拒绝类异常（如池已 shutdown）同样不是「执行失败」，如实说明未执行。
                "重载未能提交（执行器不可用，本次未执行）：${e.javaClass.simpleName}: ${e.message ?: ""}"
            }
        return PluginReloadOutcome.failed(name, sanitize(reason, MAX_REASON_CHARS))
    }

    /** 等待已提交任务的结果；超时/异常/中断均折算为该监听器的失败结果。 */
    private fun awaitOutcome(
        name: String,
        future: Future<PluginReloadOutcome>,
        started: AtomicBoolean,
    ): PluginReloadOutcome =
        try {
            future.get(reloadTimeoutMs, TimeUnit.MILLISECONDS)
        } catch (e: TimeoutException) {
            future.cancel(true)
            // **排队与执行必须区分**：提交成功只代表进了队列，不代表开始跑。若到超时点仍未置位，
            // 说明它还排在被卡死线程占满的池子后面，一次都没被执行——把它报成「超时」会让运维
            // 去查那个插件为什么慢，而真正要做的是排查已卡死的线程或降低并发。
            if (started.get()) {
                PluginReloadOutcome(
                    listener = name,
                    ok = false,
                    reason = "重载超时（${reloadTimeoutMs}ms）",
                    timeout = true,
                )
            } else {
                PluginReloadOutcome.failed(
                    name,
                    "重载排队超时（${reloadTimeoutMs}ms 内未轮到执行，本次未执行）：" +
                        "执行器并发位被未响应中断的插件占满，请排查卡死插件或降低单次变更的插件数",
                )
            }
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
            failed(name, e)
        }
    }

    /**
     * 异常原因只带类型与 message（不铺堆栈），并**过脱敏**：插件异常的 message 里带回显的
     * 连接串 / token 是常见情况，而这段文本会进回执、落控制面 DB、再渲染到前端。
     */
    private fun failed(
        name: String,
        e: Throwable,
    ): PluginReloadOutcome = PluginReloadOutcome.failed(name, sanitize("${e.javaClass.simpleName}: ${e.message ?: ""}", MAX_REASON_CHARS))

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

    internal companion object {
        /**
         * 并发重载的插件数上限。
         *
         * 取 16 是因为这里排的是**同一个 MC 进程内的业务插件**，不是「服务器台数」——
         * 单服装 60+ 插件是常见形态（大厅服尤其如此），而 4 的容量会让绝大多数插件在队列里
         * 干等而不是真并发，把「插件自己慢」错报成「重载超时」。
         *
         * 不取更大的原因：卡死不响应中断的插件会永久占住工作线程（线程无法强杀），
         * 池越大只是把「什么时候被占满」往后推，并不解决问题；真正的解法是 [REJECT_POLICY]
         * 把「没轮到执行」如实报出来，而不是让它伪装成超时。
         */
        const val POOL_SIZE = 16

        /**
         * 等待队列容量：与 [POOL_SIZE] 同量级。
         *
         * **必须有界**。无界队列会让「排在第 17 个之后」的插件一直排队，而它的超时计时
         * 从 `future.get` 就开始——于是它一次都没被执行过，却报「重载超时（Nms）」，
         * 运维会去查那个插件为什么卡住，而真相是它根本没轮到。有界队列 + [REJECT_POLICY]
         * 才能把这种情况如实说成「执行器饱和，本次未执行」。
         */
        const val QUEUE_CAPACITY = 16

        /**
         * 饱和策略：拒绝而非排队。
         *
         * 不用 CallerRunsPolicy：那会让提交方（交付命令线程 / 配置应用线程）亲自跑插件逻辑，
         * 等于把「单插件卡死」升级成「派发线程卡死」，比拒绝更糟。拒绝后由 [dispatch]
         * 折算成一条明确的失败结果，既不丢失「该插件未通知」这个事实，也不拖住其余插件。
         */
        val REJECT_POLICY = ThreadPoolExecutor.AbortPolicy()

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
        fun newDaemonPool(): ExecutorService = newPool(ArrayBlockingQueue(QUEUE_CAPACITY), defaultThreadFactory(), REJECT_POLICY)

        /** 统一构造形态：守护线程 + 允许核心线程超时回收（不给 agent reload 留常驻线程）。 */
        fun newPool(
            queue: BlockingQueue<Runnable>,
            factory: ThreadFactory,
            handler: RejectedExecutionHandler,
        ): ThreadPoolExecutor =
            ThreadPoolExecutor(
                POOL_SIZE,
                POOL_SIZE,
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
