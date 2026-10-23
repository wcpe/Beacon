package top.wcpe.beacon.e2e

import taboolib.common.platform.function.getDataFolder
import taboolib.common.platform.function.info
import taboolib.common.platform.function.submit
import top.wcpe.beacon.agent.api.BeaconAgentProvider
import java.io.File
import java.util.concurrent.atomic.AtomicBoolean

/**
 * 门面连通性端到端探针（Bukkit 壳）。
 *
 * 覆盖 `BeaconAgent` 上三个此前**完全没有真机断言**的入口：
 *  - `connected()`——与控制面的连接是否建立；
 *  - `effectiveMd5()`——当前生效配置的整体摘要（无配置时应为空 Optional，不是异常）；
 *  - `awaitRegistered(timeoutMillis)`——等待注册完成的阻塞式入口。
 *
 * ## awaitRegistered 为什么要单独证明
 *
 * 它是**唯一会阻塞调用线程**的门面方法，且必须满足两条契约：
 *  1. 注册完成时返回 `true` 且**远早于**超时；
 *  2. 超时参数为 0 或极小时不得卡死，应立刻返回当前事实（`false`）。
 *
 * 只断言「最终返回 true」是不够的——那无法区分「立即返回」与「卡满超时后才返回」。
 * 故观测里同时记录**耗时**，外部据此断言返回值与耗时都符合预期。
 *
 * ## 观测格式
 *
 * 写到 `e2e-facade-connectivity.log`，第 2 段取值即行语义：
 *  - `FACADE_CONNECTED`        连接状态（第 3 段为 true/false）
 *  - `FACADE_EFFECTIVE_MD5`    生效配置摘要（无配置时为空标记）
 *  - `AWAIT_REGISTERED_OK`     注册已完成（第 5 段含耗时毫秒）
 *  - `AWAIT_REGISTERED_TIMEOUT` 以极小超时探测，验证「不卡死且返回当前事实」
 *  - `FACADE_ERROR`            门面调用抛异常（契约下不应出现）
 *
 * 由环境变量 `BEACON_E2E_FACADE` 门控（非空才启用）。
 */
object FacadeConnectivityE2EProbe {
    /** 标记文件名：外部驱动据此断言。 */
    private const val OBSERVATION_FILE = "e2e-facade-connectivity.log"

    /** 轮询周期（tick，20 tick/秒）：约每 2 秒刷新一次。 */
    private const val POLL_INTERVAL_TICKS = 40L

    /** awaitRegistered 的等待上限（毫秒）：远大于正常注册耗时，用于断言「不是卡满超时才成功」。 */
    private const val AWAIT_TIMEOUT_MS = 30_000L

    /** 极小超时（毫秒）：用于验证「不会因为超时值小而卡死或抛异常」。 */
    private const val TINY_TIMEOUT_MS = 1L

    /** awaitRegistered 是否已成功过：成功后不再重复阻塞等待（避免每轮都占用 30s）。 */
    private val registered = AtomicBoolean(false)

    /** 是否已落过「极小超时」观测（该场景结论稳定，落一次即可，不必每轮重复）。 */
    private val tinyProbed = AtomicBoolean(false)

    /** 启动探针：清旧标记 → 启异步周期刷新。 */
    fun start() {
        if (System.getenv("BEACON_E2E_FACADE").isNullOrBlank()) {
            info("未配置 BEACON_E2E_FACADE，跳过门面连通性探针")
            return
        }
        val markFile = File(getDataFolder(), OBSERVATION_FILE)
        // 清空上轮残留，保证每次 run 的标记文件只含本轮观测。
        if (markFile.exists()) {
            markFile.delete()
        }
        registered.set(false)
        tinyProbed.set(false)
        info("Beacon E2E 门面连通性探针已启用，标记文件=${markFile.absolutePath}")
        submit(async = true, delay = POLL_INTERVAL_TICKS, period = POLL_INTERVAL_TICKS) {
            probeOnce(markFile)
        }
    }

    /** 探一次连通性三入口。 */
    private fun probeOnce(markFile: File) {
        if (!BeaconAgentProvider.isAvailable()) {
            return
        }
        val agent = BeaconAgentProvider.get()

        runCatching {
            E2EObservation.append(markFile, "FACADE_CONNECTED", agent.connected().toString(), "identity=${agent.identity().serverId()}", "")
        }.onFailure {
            E2EObservation.append(markFile, "FACADE_ERROR", "connected", "-", "err=${it.javaClass.simpleName}: ${it.message}")
        }

        runCatching {
            val md5 = agent.effectiveMd5().orElse(null)
            // 无配置时是空 Optional——这是合法状态，不得当作异常或缺失。
            E2EObservation.append(markFile, "FACADE_EFFECTIVE_MD5", md5 ?: "EMPTY", "-", "")
        }.onFailure {
            E2EObservation.append(markFile, "FACADE_ERROR", "effectiveMd5", "-", "err=${it.javaClass.simpleName}: ${it.message}")
        }

        probeAwaitRegistered(markFile, agent)
    }

    /**
     * 探测 awaitRegistered 的两条契约。
     *
     * 顺序很重要：先做极小超时探测（此时可能确实未注册，返回 false 是**正确**的），
     * 再做正常超时等待。反过来会让极小超时探测恰好命中「已注册」而失去意义。
     */
    private fun probeAwaitRegistered(
        markFile: File,
        agent: top.wcpe.beacon.agent.api.BeaconAgent,
    ) {
        if (!tinyProbed.getAndSet(true)) {
            val startedAt = System.nanoTime()
            runCatching { agent.awaitRegistered(TINY_TIMEOUT_MS) }
                .onSuccess { ok -> recordAwait(markFile, "AWAIT_REGISTERED_TIMEOUT", ok, startedAt, TINY_TIMEOUT_MS) }
                .onFailure {
                    E2EObservation.append(
                        markFile,
                        "FACADE_ERROR",
                        "awaitRegistered-tiny",
                        "-",
                        "err=${it.javaClass.simpleName}: ${it.message}",
                    )
                }
        }

        if (registered.get()) {
            return
        }
        val startedAt = System.nanoTime()
        runCatching { agent.awaitRegistered(AWAIT_TIMEOUT_MS) }
            .onSuccess { ok ->
                // 只有真正成功才停手；false 说明还没就绪，下一轮继续等。
                if (ok) {
                    registered.set(true)
                    recordAwait(markFile, "AWAIT_REGISTERED_OK", true, startedAt, AWAIT_TIMEOUT_MS)
                }
            }
            .onFailure {
                E2EObservation.append(
                    markFile,
                    "FACADE_ERROR",
                    "awaitRegistered",
                    "-",
                    "err=${it.javaClass.simpleName}: ${it.message}",
                )
            }
    }

    /**
     * 落一条 awaitRegistered 观测，**必须带耗时**。
     *
     * 外部据此断言：注册成功时耗时远小于超时上限（证明是「立即返回」而非「卡满才返回」）。
     */
    private fun recordAwait(
        markFile: File,
        source: String,
        ok: Boolean,
        startedAtNanos: Long,
        timeoutMs: Long,
    ) {
        val elapsedMs = (System.nanoTime() - startedAtNanos) / 1_000_000
        E2EObservation.append(
            markFile,
            source,
            ok.toString(),
            timeoutMs.toString(),
            "elapsedMs=$elapsedMs;timeoutMs=$timeoutMs",
        )
    }
}
