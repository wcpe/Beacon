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
    /** 初始并发位数量（FR-280）：由 AgentSettings.configReloadPoolSize 注入，收口到 [MIN_POOL_SIZE, MAX_POOL_SIZE]。 */
    poolSize: Int = DEFAULT_POOL_SIZE,
) {
    /**
     * 超时预算：**可变**，因为控制面可在运行期下发新值（FR-278 热重载）。
     *
     * 用 AtomicLong 而不是普通字段：派发线程读它、配置应用线程写它，
     * 必须让新值对下一轮派发立即可见（普通 Long 在 JVM 上不保证可见性）。
     */
    private val timeoutMs = AtomicLong(reloadTimeoutMs)

    /**
     * 并发位数量（FR-280）：**可变**，因为控制面可在运行期下发新值（FR-278 热重载）。
     *
     * 与 [timeoutMs] 的关键差别：超时只是个等待时长，改了不影响执行载体；并发位是线程池的
     * **构造属性**，改它必须换池（见 [updatePoolSize]）。
     *
     * 初始值同样收口：容量 <= 0 会让 ThreadPoolExecutor 构造直接抛异常，而「下游配置写错」
     * 不该把整条派发路径打死——这里退到 [MIN_POOL_SIZE] 继续工作。
     */
    @Volatile
    private var currentPoolSize: Int = poolSize.coerceIn(MIN_POOL_SIZE, MAX_POOL_SIZE)

    /**
     * 当前生效的并发位数（已收口）。
     *
     * 供诊断与测试读取。暴露它的理由很具体：收口是「不得假报」的一部分——下发 9999 必须真的
     * 变成 [MAX_POOL_SIZE]，不能只是"看起来接受了、建池时照用原值"。没有这个读口，测试就只能
     * 靠开几百个线程去数并发度，既慢又不稳。
     */
    internal val effectivePoolSize: Int get() = currentPoolSize

    /** 外部注入的池（生命周期归注入方）；null 表示由本类按需自建。 */
    private val injectedPool: ExecutorService? = executor

    @Volatile
    private var selfBuiltPool: ExecutorService? = null

    /**
     * 已退役、但仍可能持有在途任务的旧池（[updatePoolSize] 换池时收编）。
     *
     * ## 为什么必须持有引用
     *
     * 换池用 `shutdown()` 优雅退役（在途 reload 跑完才算数），但「跑完」没有时限：插件卡死
     * 不响应中断时，旧池里的线程会一直活着。若换池后直接丢掉引用，[close] 的 `shutdownNow`
     * 就只覆盖当前那个池：
     *  - 旧池队列里在等的 reload 会在 agent **已停用之后**继续执行插件逻辑，与 [close] 注释
     *    「已排队的任务没有理由继续执行」直接冲突；
     *  - 线程永久泄漏：`allowCoreThreadTimeOut` 只回收空闲线程，卡死的回收不掉，且无句柄可事后干预。
     *
     * 故退役池必须留在手里，直到 [close] 把它们一并打断或它们自己终止。
     *
     * ## 并发约定
     *
     * 与 [selfBuiltPool] 由**同一把** `synchronized(this)` 保护（不引入第二把锁，否则换池与关池
     * 可能出现撕裂的中间态）。集合不会无限增长：换池时顺手清掉已终止的，[close] 时整集合清空。
     */
    private val retiredPools = mutableSetOf<ExecutorService>()

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
     *
     * [budgetMs] 是本次派发已读定的预算，**必须下传**而不是在这里重读 [timeoutMs]：
     * 重读会让同一个派发批次里出现两副面孔（按旧值建池、按新值 0 建 collector →
     * 整批立刻判超时；或反之退回内联把调用线程堵住）。
     */
    private fun currentExecutor(budgetMs: Long): ExecutorService? = injectedPool ?: selfBuiltOrNull(budgetMs)

    /**
     * 未注入时按需自建；未启用超时（<=0）或已关闭返回 null。
     *
     * 返回 null 表示「内联执行」——但注意关闭后内联执行也是不希望的，
     * 故 [dispatchAll] 在关闭时直接报「未执行」而不是退回内联（见该函数）。
     */
    private fun selfBuiltOrNull(budgetMs: Long): ExecutorService? {
        if (closed || budgetMs <= 0L) {
            return null
        }
        return selfBuiltPool ?: synchronized(this) {
            // 双检必须在锁内复查 closed：建池与关池互斥，否则 close() 可以在
            // 「通过检查」与「赋给 selfBuiltPool」之间完整跑完，留下一个关闭后才建好、
            // 且永远不会被 shutdownNow 的池（关闭语义必须单向）。
            if (closed) null else selfBuiltPool ?: newDaemonPool(currentPoolSize).also { selfBuiltPool = it }
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
     * 已随本次 reload 过期了，跑完只会把旧配置写进新进程。同理，热重载换掉的那些**退役池**
     * 也必须一并 `shutdownNow`（见 [retiredPools]）：只关当前池会让它们队列里的 reload 在
     * agent 已停用之后继续跑插件逻辑，线程也会永久漏掉。
     *
     * **必须与建池持同一把锁**：`selfBuiltOrNull` 是锁内 check-then-act，若这里不加锁，
     * 另一个线程能在「复查 closed」与「赋值 selfBuiltPool」之间插入，留下一个关闭后新建的池。
     */
    fun close() {
        synchronized(this) {
            closed = true
            // 当前池 + 全部退役池：一个都不能漏（漏掉的那个就是「停机后还在跑插件」的来源）。
            (retiredPools + listOfNotNull(selfBuiltPool)).forEach { it.shutdownNow() }
            retiredPools.clear()
            selfBuiltPool = null
        }
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

    /**
     * 热更新并发位数（FR-278）：旧池优雅退役、在途任务跑完，新池接管后续派发。
     *
     * ## 为什么不是 `shutdownNow`
     *
     * 改并发位不该丢任务：此刻正在执行的是**有意义的**业务插件 reload（控制面已下发新配置、
     * 插件正在应用它），中断它等于让「调了个参数」变成「一批插件重载失败」——正是本功能
     * 要避免的那种"改配置反而搞坏线上"的形态。故旧池用 `shutdown()`：不再接收新提交，
     * 但已排队与在跑的都会跑完。
     *
     * [close] 保持 `shutdownNow` 不变：停机时目标配置已随本次停用过期，让排队任务跑完才是错的。
     * 两者语义刻意不同，不可互相复用。
     *
     * 退役池在换池时被收编进 [retiredPools] 而不是直接丢掉引用：优雅退役没有时限，得有人
     * 在 [close] 时把它们一并 `shutdownNow`，否则「改并发位」会变成一条停机也关不掉的任务通道。
     *
     * ## 为什么把 [selfBuiltPool] 置空而不是预先建好新池
     *
     * 建池只有 [selfBuiltOrNull] 一条路径（惰性、锁内、复查 closed）。为热重载再开一条建池
     * 旁路，等于让「初始建池」与「热重载建池」两处初始化逻辑各演化一次；置空后下一次派发
     * 自然按新容量建池，路径唯一。
     *
     * ## 值未变化时不重建
     *
     * 每换一次池都要起一批新线程、并让旧池线程等到空闲回收。周期下发会反复送同一个值，
     * 无谓抖动没有收益。
     *
     * ## 已关闭时不做任何事
     *
     * 关闭语义必须单向（见 [closed]）：否则停机后一次下发就能让派发重新跑起来。
     *
     * @param newPoolSize 目标并发位数，收口到 [MIN_POOL_SIZE, MAX_POOL_SIZE]
     */
    fun updatePoolSize(newPoolSize: Int) {
        val sanitized = newPoolSize.coerceIn(MIN_POOL_SIZE, MAX_POOL_SIZE)
        synchronized(this) {
            // 这一把锁与 [close]/[selfBuiltOrNull] 是同一把：重建与关池必须互斥，
            // 否则「刚建好的新池」可能在 close() 之后被赋给 selfBuiltPool 而永不被 shutdown。
            if (closed || sanitized == currentPoolSize) {
                return
            }
            currentPoolSize = sanitized
            // 顺手清理已彻底终止的退役池：既让「反复改并发位」不会让集合无限增长，
            // 也不必等 close() 才回收。只清 isTerminated 的——还在跑的不许丢引用（见 retiredPools）。
            retiredPools.removeAll { it.isTerminated }
            // 优雅退役：只停新提交，不打断在途任务（与 close 的 shutdownNow 刻意不同）。
            // 但**必须收编进 retiredPools**：否则这个池从此刻起无人持有，close() 关不到它，
            // 队列里在等的 reload 会在停机之后继续执行。
            selfBuiltPool?.let { retired ->
                retired.shutdown()
                retiredPools.add(retired)
            }
            selfBuiltPool = null
        }
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
        // 超时预算**只读一次**贯穿本次派发：读两次会在热重载把值改成 0 的瞬间出现
        // 「按旧值建好了池、却按新值 0 建 collector」→ deadline = now + 0 → 整批立刻判超时，
        // 而任务其实正在执行（假失败）。
        val budgetMs = timeoutMs.get()
        // 已关闭时整批预置为「未执行」：既不能建池，也不能退回内联执行——否则停机后派发
        // 仍在跑插件逻辑，而调用方以为已经停了（关闭语义必须单向、可见）。
        // 用 IllegalStateException 而非 RejectedExecutionException：后者会被收口器判成
        // 「执行器饱和」，运维会为一个「agent 正在停机」的状态去调 reload-pool-size。
        val closedSubmitted =
            if (closed) {
                val reason = IllegalStateException(CLOSED_REASON)
                requests.map { req ->
                    ReloadOutcomeCollector.Submitted(
                        req.listener.javaClass.name,
                        Result.failure(reason),
                        AtomicBoolean(false),
                    )
                }
            } else {
                null
            }
        // 空请求不建池：惰性取池，避免为一次空派发留下线程组。
        val pool = if (closed || requests.isEmpty()) null else currentExecutor(budgetMs)
        return when {
            requests.isEmpty() -> emptyList()
            closedSubmitted != null -> ReloadOutcomeCollector(budgetMs).collectAll(closedSubmitted)
            // 内联是没有并发载体时**刻意保留**的兼容行为（config-reload-timeout-ms <= 0）。
            pool == null -> requests.map { invoke(it.listener, it.changedDataIds, it.newMd5) }
            // 先**全部提交**（绝不中途等待），再交给收口器统一收。
            // collector 每轮新建：超时值可能刚被热重载改过，复用旧实例会沿用旧预算。
            else -> ReloadOutcomeCollector(budgetMs).collectAll(requests.map { submitOne(pool, it) })
        }
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
            }.recoverCatching { e ->
                // 通过 closed 检查之后、submit 之前，池可能被 shutdown，共两种来源：
                //   1) close() 的 shutdownNow（agent 正在停机）；
                //   2) updatePoolSize() 的 shutdown（并发位热重载，旧池优雅退役）。
                // 必须在文案上分开：把一次「改并发位」报成「agent 正在停机」，运维会去查一个
                // 并不存在的停机事件；而「执行器饱和」更错——饱和是容量不足，这里是池已退役。
                if (e is RejectedExecutionException && pool.isShutdown) {
                    throw IllegalStateException(if (closed) CLOSED_REASON else RETIRED_REASON, e)
                }
                throw e
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
                    reason = sanitizeForReceipt(result.reason(), MAX_REASON_CHARS),
                    detailJson = sanitizeForReceipt(result.detailJson(), MAX_DETAIL_CHARS),
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
            PluginReloadOutcome.failed(name, sanitizeForReceipt("${e.javaClass.simpleName}: ${e.message ?: ""}", MAX_REASON_CHARS))
        }
    }

    /** 一条待办的重载请求（批量派发的入参）。 */
    data class ReloadRequest(
        val listener: ConfigChangeListener,
        val changedDataIds: Set<String>,
        val newMd5: String,
    )

    internal companion object {
        /**
         * 已关闭时的「未执行」原因文案。
         *
         * 必须与「执行器饱和」区分开：饱和是可调参的容量问题，已关闭是停机事实。
         * 混为一谈会让运维为一个「agent 正在停机」的状态去调 `delivery.reload-pool-size`。
         */
        const val CLOSED_REASON = "执行器已关闭（agent 正在停机，本次未执行）"

        /**
         * 旧池刚被 [updatePoolSize] 优雅退役时的「未执行」原因文案。
         *
         * 只在「取池之后、提交之前恰好发生并发位热重载」这一极窄竞态下出现。文案必须与
         * [CLOSED_REASON] 分开：agent 并没有停机，只是一次调参把旧池退役了——运维此时该做的是
         * 重试或核对下发，而不是去查停机。
         */
        const val RETIRED_REASON = "执行器已因并发位热重载退役（本次未执行，请重试）"

        /**
         * 并发位的下界：至少 1，否则线程池无法构造（容量 0 直接抛异常）。
         *
         * 下限不取 0：并发 0 语义上等于「永不执行」，而需要停用 agent 有 [close] 这条明确路径。
         */
        const val MIN_POOL_SIZE = 1

        /**
         * 并发位的上界：256。
         *
         * 与 `AgentBootstrap` 读取 config.yml 时的收口口径、控制面元数据里的 max 保持一致
         * （三处同值，任一处放宽都会让「下发值」与「实际容量」对不上）。
         *
         * 上界不是为了保守，而是为了让「泄漏/卡死」有界可见：每个并发位在饱和时都是一个
         * 真实线程，几百个卡死插件同时占位会把 MC 进程的内存与句柄拖垮，而病根是插件不响应
         * 中断，调大并发位只会掩盖它。
         */
        const val MAX_POOL_SIZE = 256

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

/**
 * 把不可信文本处理成可安全进回执的形态：先脱敏再限长。
 *
 * 顺序不可颠倒——先截断后脱敏可能把 `token=xxxx` 截成 `token=xx`，脱敏规则匹配不到，
 * 反而漏出半截密钥。
 *
 * 放在类外（顶层私有）而非 [ReloadDispatcher] 的成员：它是纯函数、不碰任何实例状态，
 * 而派发器本身已贴着 detekt 的类内函数数上限——热重载要再加一个 `updatePoolSize`，
 * 把一个与状态无关的格式化工具搬出类外是比合并语义更干净的解法（不用 @Suppress）。
 */
private fun sanitizeForReceipt(
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
    return redacted.take(maxChars) + ReloadDispatcher.TRUNCATED_MARK
}
