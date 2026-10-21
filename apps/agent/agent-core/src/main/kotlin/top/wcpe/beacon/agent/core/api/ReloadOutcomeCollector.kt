package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.core.log.LogRedactor
import java.util.concurrent.ExecutionException
import java.util.concurrent.Future
import java.util.concurrent.RejectedExecutionException
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean

/**
 * 把「一批已提交的重载任务」收成结构化结果。
 *
 * ## 为什么单独成类
 *
 * 「提交」和「收结果」是两件事：前者关心池容量与拒绝策略，后者关心超时、中断、
 * 排队与执行的区分。混在一个类里会让那个类函数数持续膨胀（detekt TooManyFunctions），
 * 更重要的是两种变化的理由不同——调整容量不该牵动超时语义，反之亦然。
 *
 * ## 核心职责：把「没结果」拆成三种本质不同的原因
 *
 * | 情形 | 语义 | `timeout` |
 * |---|---|---|
 * | 提交被拒（池满 / 已关闭） | 根本**没进队列** | false |
 * | 排队到超时仍未被取走 | 进了队列但**没轮到执行** | false |
 * | 真跑过但没跑完 | 执行了但**没跑完** | true |
 *
 * 三者运维要做的事完全不同（查并发/卡死线程 vs 查插件为什么慢）。混成一句「重载超时」
 * 会把排查方向带偏，这正是护栏「不得假报成功」的同源要求：不要说一件没发生的事。
 *
 * @param timeoutMs 单个监听器的超时预算（毫秒）；批量时作为**总**预算。
 */
internal class ReloadOutcomeCollector(
    private val timeoutMs: Long,
) {
    /**
     * 统一收集一批已提交任务的结果，共用一个总截止时刻。
     *
     * ## 关键：已完成的项必须立刻取到，不能被排在前面的慢任务挡住
     *
     * 「按列表顺序 get、每项算剩余时间」是错的：第一个卡住的项会吃光整个预算，
     * 后面早已完成的项也会被判超时。实测表现就是「卡死的插件 + 秒回的插件」双双被标 timeout。
     *
     * 这里先做一遍**非阻塞**收割（`isDone` 为真即取，绝不等），再对仍未完成的项按剩余预算等待。
     */
    fun collectAll(submitted: List<Submitted>): List<PluginReloadOutcome> {
        val deadline = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(timeoutMs)
        val outcomes = arrayOfNulls<PluginReloadOutcome>(submitted.size)

        // 提交阶段就失败的项先落定（它们没有 future 可取）。
        submitted.forEachIndexed { index, item ->
            if (item.isFailure) {
                outcomes[index] = rejected(item.name, item.exceptionOrNull()!!)
            }
        }

        // **轮转收割**，而不是按列表顺序死等第一个。
        //
        // 按顺序死等是错的：第一个卡住的项一旦吃光预算，后面早已完成的项就只能判超时——
        // 实测表现就是「卡死的插件 + 秒回的插件」双双被标 timeout。
        //
        // 这里循环扫「还没落定的项」，每轮只对**已完成的**取值，只要还有**未完成**的就短暂等待；
        // 直到全部落定或预算耗尽。这样秒回的项在下一轮就被收走，不会被慢任务挡住。
        var pending = submitted.indices.filter { outcomes[it] == null }
        while (pending.isNotEmpty()) {
            // 1) 先非阻塞收割本轮已完成的。
            val stillPending = mutableListOf<Int>()
            for (index in pending) {
                val item = submitted[index]
                if (item.isDone) {
                    outcomes[index] = outcomeOfDone(item)
                } else {
                    stillPending += index
                }
            }
            pending = stillPending
            // 2) 仍有未完成的：等一小片时间让状态前进，再回到循环收割。
            //    预算耗尽时 waitNoLongerThan 返回 0，下一次收割会拿到最终状态（可能仍为空，
            //    由循环外的收口统一判超时），因此这里不需要第二个 break。
            if (pending.isNotEmpty()) {
                val remaining = deadline - System.nanoTime()
                awaitProgress(submitted, pending, remaining.coerceAtLeast(0L))
                if (remaining <= 0L) {
                    // 预算已耗尽且仍有未落定的：不再等待，直接退出循环交给收口。
                    pending = emptyList()
                }
            }
        }

        // 仍未落定的：按超时/未执行收口（这些是真的没在预算内完成）。
        pending.forEach { index -> outcomes[index] = onTimeout(submitted[index]) }

        return outcomes.mapIndexed { index, outcome -> outcome ?: onTimeout(submitted[index]) }
    }

    /**
     * 等到 pending 里**任意一个**任务完成，或预算耗尽。
     *
     * 不返回结果，只负责「让时间过去一点、让状态前进一点」——下一轮循环会重新收割，
     * 这样既不会漏掉已完成项，也不会因某一个慢任务卡住其余项。
     */
    private fun awaitProgress(
        submitted: List<Submitted>,
        pending: List<Int>,
        remainingNanos: Long,
    ) {
        // 分片等待：单片不超过 10ms，保证「谁先完成」能较快被下一轮收割看到，
        // 而不是一直等到某个特定项完成。
        val slice = minOf(remainingNanos, TimeUnit.MILLISECONDS.toNanos(10))
        val first = submitted[pending.first()]
        runCatching { first.getOrThrow().get(slice, TimeUnit.NANOSECONDS) }
    }

    /** 取一个**已完成**任务的结果（不阻塞）；异常折算为该监听器失败。 */
    private fun outcomeOfDone(item: Submitted): PluginReloadOutcome =
        try {
            item.getOrThrow().get()
        } catch (e: ExecutionException) {
            failed(item.name, e.cause ?: e)
        } catch (e: Throwable) {
            failed(item.name, e)
        }

    /**
     * 超时收口：区分「跑过但没跑完」与「根本没轮到」。
     *
     * 这两者运维要做的事完全不同（查插件慢 vs 查并发/卡死线程），混成一句话会把排查方向带偏。
     */
    private fun onTimeout(item: Submitted): PluginReloadOutcome {
        item.cancel()
        return if (item.started.get()) {
            PluginReloadOutcome(
                listener = item.name,
                ok = false,
                reason = "重载超时（${timeoutMs}ms）",
                timeout = true,
            )
        } else {
            PluginReloadOutcome.failed(
                item.name,
                "重载排队超时（${timeoutMs}ms 内未轮到执行，本次未执行）：" +
                    "执行器并发位被未响应中断的插件占满，请排查卡死插件或降低单次变更的插件数",
            )
        }
    }

    /**
     * 提交阶段就被拒（池已饱和 / 已关闭）的收口。
     *
     * **必须与「超时」区分开**：超时意味着「跑过但没跑完」；而这里根本**没被执行过**。
     */
    private fun rejected(
        name: String,
        e: Throwable,
    ): PluginReloadOutcome {
        val saturated = e is RejectedExecutionException
        val reason =
            if (saturated) {
                "重载执行器饱和（${ReloadDispatcher.POOL_SIZE} 个并发位与 " +
                    "${ReloadDispatcher.QUEUE_CAPACITY} 个排队位已满，本次未执行）：" +
                    "请排查已卡死未响应中断的插件，或调大执行器容量"
            } else {
                // 非拒绝类异常（如池已 shutdown）同样不是「执行失败」，如实说明未执行。
                "重载未能提交（执行器不可用，本次未执行）：${e.javaClass.simpleName}: ${e.message ?: ""}"
            }
        return PluginReloadOutcome.failed(name, sanitize(reason, ReloadDispatcher.MAX_REASON_CHARS))
    }

    /** 异常原因只带类型与 message（不铺堆栈），并**过脱敏**。 */
    private fun failed(
        name: String,
        e: Throwable,
    ): PluginReloadOutcome =
        PluginReloadOutcome.failed(
            name,
            sanitize("${e.javaClass.simpleName}: ${e.message ?: ""}", ReloadDispatcher.MAX_REASON_CHARS),
        )

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
        return redacted.take(maxChars) + ReloadDispatcher.TRUNCATED_MARK
    }

    /**
     * 一条已提交任务：监听器名 + 提交结果 + 「是否真的开始执行」标志。
     *
     * `future` 是 `Result<Future<...>>`：提交本身可能被拒（池满/已关闭），
     * 那属于「未执行」，必须与「执行失败」「超时」区分开。
     */
    internal class Submitted(
        val name: String,
        val future: Result<Future<PluginReloadOutcome>>,
        val started: AtomicBoolean,
    ) {
        /** 提交失败（池满 / 已关闭）：属于「未执行」。 */
        val isFailure: Boolean get() = future.isFailure

        /** 任务是否已完成（非阻塞判断，用于先收割已就绪的结果）。 */
        val isDone: Boolean get() = future.getOrNull()?.isDone ?: false

        /** 取 future；调用方须先用 [isFailure] 排除提交失败的项。 */
        fun getOrThrow(): Future<PluginReloadOutcome> = future.getOrThrow()

        /** 提交失败的原因。 */
        fun exceptionOrNull(): Throwable? = future.exceptionOrNull()

        /** 尽力取消；提交失败的项无事可做。 */
        fun cancel() {
            runCatching { future.getOrNull()?.cancel(true) }
        }
    }
}
