package top.wcpe.beacon.e2e

import taboolib.common.platform.function.console
import taboolib.common.platform.function.getDataFolder
import taboolib.common.platform.function.info
import taboolib.common.platform.function.submit
import java.io.File

/**
 * FR-276「零配置接入」真机验收探针（Bukkit 壳）。
 *
 * ## 它存在的理由
 *
 * `/beacon join <地址> <令牌>` 是 FR-276 的唯一入口，但它是**控制台命令**：
 * Go 侧的 e2e harness 只通过 Gradle 拉起 Paper 进程（mc-testkit 的 serve 任务既没有
 * stdin 通道，也没有 RCON 之类的外部入口），无法把一条命令送进服务端控制台。
 * 故沿用本仓库既有的探针范式：外部驱动把「要 join 的参数」写成文件，
 * 本探针在主线程代为经控制台派发，并把派发结果写成观测行，供外部断言「命令真的被执行了」。
 *
 * ## 为什么用 console().performCommand 而不是 Bukkit API
 *
 * agent 自身的受限命令派发（FR-15）走的就是这条 TabooLib 跨平台入口
 * （见 `BukkitPlatformAdapter.dispatchConsoleCommand`）。复用它有两个好处：
 *  1. 走的是**真正的服务端命令派发**（与运维在控制台敲命令同一条路径），
 *     因而能一并验证命令注册、权限门与参数解析，而不是绕过命令直接写凭据；
 *  2. 本模块不需要引入 org.bukkit（与 agent 保持一致：不硬链平台实现）。
 *
 * ## 安全约束
 *
 * - **绝不把接入令牌写进观测文件或日志**：观测文件位于 mc-testkit 运行目录，
 *   CI 的归档敏感内容扫描会扫到这里，令牌在此出现等同泄漏。故只记地址与派发结果。
 * - 派发成功后**立即删除请求文件**，不让令牌在磁盘上滞留。
 * - 只派发 `beacon join <地址> <令牌>` 这一种固定形态，不提供任意命令执行能力。
 *
 * ## 线程模型（为什么是状态机而不是循环重试）
 *
 * `performCommand` 必须在主线程执行，而主线程**绝不能被阻塞**（阻塞会冻住整个服务端，
 * 让所有断言一起超时、根因被埋掉）。因此这里不用「主线程里 sleep 重试」，
 * 而是把重试摊到多轮异步轮询上：每轮最多提交一次主线程派发，结果经 volatile 回传，
 * 下一轮据此决定「成功收尾」还是「再试一次」。
 */
object JoinE2EProbe {
    /** 外部驱动投递 join 参数的请求文件名（位于本插件数据目录）。 */
    const val REQUEST_FILE = "e2e-join-request.txt"

    /** 观测标记文件名：外部驱动据此断言命令是否真的派发到了控制台。 */
    const val OBSERVATION_FILE = "e2e-join.log"

    /** 门控环境变量：仅在显式开启时启动探针。 */
    private const val ENABLE_ENV = "BEACON_E2E_JOIN"

    /** 轮询周期（tick，20 tick/秒）：约每 1 秒推进一轮。 */
    private const val POLL_INTERVAL_TICKS = 20L

    /**
     * 最大派发尝试轮数。
     *
     * 需要重试是因为「请求文件已投递」可能早于 BeaconAgent 完成命令注册
     * （本探针与 agent 同批启用，但 agent 的 join 命令在其 enable 里注册）。
     * `performCommand` 返回 false 即「命令不存在」，此时下一轮再试。
     */
    private const val MAX_ATTEMPTS = 60

    /** 已解析到的命令（**含令牌**，只留在内存里，绝不落盘/打日志）。 */
    @Volatile
    private var command: String? = null

    /** 命令里的控制面地址（可安全记录，用于观测与日志）。 */
    @Volatile
    private var endpoint: String? = null

    /** 最近一次主线程派发的返回值；null 表示尚无结果或正在派发途中。 */
    @Volatile
    private var lastAccepted: Boolean? = null

    /** 是否已成功派发（成功后不再做任何事，避免重复执行 join）。 */
    @Volatile
    private var finished = false

    /** 已尝试派发的轮数。 */
    private var attempts = 0

    /** 已消费过的请求文件原文，用于去重（同一份请求只解析一次）。 */
    @Volatile
    private var consumedRaw: String? = null

    /** 启动探针；未设置门控环境变量时直接返回（探针不上线）。 */
    fun start() {
        if (System.getenv(ENABLE_ENV).isNullOrBlank()) {
            info("Beacon E2E join 探针未启用（缺 $ENABLE_ENV）")
            return
        }
        val observation = File(getDataFolder(), OBSERVATION_FILE)
        // 清空上轮残留，保证每次 run 的标记文件只含本轮观测。
        if (observation.exists()) {
            observation.delete()
        }
        E2EObservation.append(observation, "PROBE_START", REQUEST_FILE, "-", "join 代执行探针已启动，等待请求文件")
        info("Beacon E2E join 探针已启动，请求文件=${File(getDataFolder(), REQUEST_FILE).absolutePath}")
        submit(async = true, delay = POLL_INTERVAL_TICKS, period = POLL_INTERVAL_TICKS) {
            poll(observation)
        }
    }

    /** 每轮推进一小步：收结果 → 读请求 → 提交一次主线程派发。 */
    private fun poll(observation: File) {
        if (finished) {
            return
        }
        if (!step(observation)) {
            finished = true
            deleteRequestFile()
        }
    }

    /**
     * 单轮状态机推进。
     *
     * @return false 表示已收尾（成功或放弃），后续轮次不再动作
     */
    private fun step(observation: File): Boolean {
        val proceed = updateFromLastResult(observation)
        if (proceed) {
            // 还没拿到命令就去读请求文件（读不到只是「本轮没请求」，不是收尾），
            // 已有命令则提交一次主线程派发。
            if (command == null) {
                tryLoadRequest(observation)
            } else {
                submitAttempt()
            }
        }
        return proceed
    }

    /**
     * 消化上一轮主线程派发的结果。
     *
     * @return false 表示已收尾（成功派发，或重试耗尽放弃）
     */
    private fun updateFromLastResult(observation: File): Boolean {
        var proceed = true
        when (lastAccepted) {
            true -> {
                E2EObservation.append(
                    observation,
                    "JOIN_DISPATCHED",
                    REQUEST_FILE,
                    "-",
                    "控制台已接受 join 命令（地址=$endpoint，共尝试 $attempts 轮）",
                )
                info("Beacon E2E join 探针已派发 join 命令到控制台（地址=$endpoint）")
                proceed = false
            }
            false -> {
                // 上一次尝试被服务端拒绝（通常是命令尚未注册）：清掉结果，下轮再来。
                lastAccepted = null
                if (attempts >= MAX_ATTEMPTS) {
                    E2EObservation.append(
                        observation,
                        "JOIN_DISPATCH_FAILED",
                        REQUEST_FILE,
                        "-",
                        "控制台始终未接受 join 命令（已尝试 $attempts 轮，可能命令未注册）",
                    )
                    proceed = false
                }
            }
            // 尚无结果（或从未派发过），继续往下走。
            null -> Unit
        }
        return proceed
    }

    /**
     * 尝试从请求文件装载 join 命令。
     *
     * **读不到不是收尾**：请求文件由外部驱动在本轮启动后才投递，前若干轮必然读不到。
     * 装载成功与否只体现在 [command] 是否被赋值上——刻意不返回布尔值，
     * 避免调用方把「还没请求」误当成「可以结束了」。
     */
    private fun tryLoadRequest(observation: File) {
        val requestFile = File(getDataFolder(), REQUEST_FILE)
        val raw =
            if (requestFile.exists()) {
                runCatching { requestFile.readText(Charsets.UTF_8) }.getOrNull()
            } else {
                null
            }
        // 只有「非空且与上次不同」才算新请求：探针每个周期都会读到同一个文件。
        raw?.takeIf { it.isNotBlank() && it != consumedRaw }?.let { fresh ->
            consumedRaw = fresh
            val request = parse(fresh)
            if (request == null) {
                E2EObservation.append(
                    observation,
                    "REQUEST_INVALID",
                    REQUEST_FILE,
                    "-",
                    "请求文件缺少 endpoint= 或 token= 行",
                )
            } else {
                // 命令含令牌，只留在内存里。
                command = "beacon join ${request.first} ${request.second}"
                endpoint = request.first
                E2EObservation.append(observation, "REQUEST_SEEN", REQUEST_FILE, "-", "已读到 join 请求，地址=${request.first}")
            }
        }
    }

    /** 提交一次主线程派发；已有一轮在途时不再重复提交。 */
    private fun submitAttempt() {
        val pending = command ?: return
        if (lastAccepted != null) {
            return
        }
        attempts++
        submit(async = false) {
            // 主线程：一次尝试，不阻塞、不重试（重试由下一轮 poll 决定）。
            lastAccepted = runCatching { console().performCommand(pending) }.getOrDefault(false)
        }
    }

    /** 派发成功后删除请求文件：不让令牌在磁盘上滞留、也不会被下一轮重复执行。 */
    private fun deleteRequestFile() {
        command = null
        runCatching { File(getDataFolder(), REQUEST_FILE).delete() }
    }

    /** 解析 `endpoint=` / `token=` 两行格式的请求；缺任一必填项返回 null。 */
    private fun parse(raw: String): Pair<String, String>? {
        var ep: String? = null
        var token: String? = null
        raw.lineSequence().forEach { line ->
            val trimmed = line.trim()
            when {
                trimmed.startsWith(ENDPOINT_KEY) -> ep = trimmed.removePrefix(ENDPOINT_KEY).trim()
                trimmed.startsWith(TOKEN_KEY) -> token = trimmed.removePrefix(TOKEN_KEY).trim()
            }
        }
        val endpointValue = ep?.takeIf { it.isNotBlank() }
        val tokenValue = token?.takeIf { it.isNotBlank() }
        return if (endpointValue != null && tokenValue != null) endpointValue to tokenValue else null
    }

    private const val ENDPOINT_KEY = "endpoint="
    private const val TOKEN_KEY = "token="
}
