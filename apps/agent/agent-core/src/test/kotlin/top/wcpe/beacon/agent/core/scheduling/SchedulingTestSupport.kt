package top.wcpe.beacon.agent.core.scheduling

import top.wcpe.beacon.agent.core.client.CandidateEntry
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.BackoffSettings
import top.wcpe.beacon.agent.core.settings.FileTreeSettings
import top.wcpe.beacon.agent.core.settings.OverrideSettings
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.io.File
import java.util.concurrent.atomic.AtomicInteger

/** 调度端点假控制面：按 URL 路由返回 canned 响应，可切各端点状态码 / 模拟连接不可达（down）。 */
class SchedFakeTransport : HttpTransport {
    @Volatile var candidatesStatus: Int = 200

    @Volatile var decideStatus: Int = 200

    @Volatile var reportStatus: Int = 202

    /** 置 true 模拟控制面不可达：execute 抛异常，被 exec 吞为 null → 各方法返回 Failed（触发降级）。 */
    @Volatile var down: Boolean = false

    val candidatesCalls = AtomicInteger(0)
    val decideCalls = AtomicInteger(0)
    val reportCalls = AtomicInteger(0)

    override fun execute(request: HttpRequest): HttpResponse {
        if (down) {
            throw RuntimeException("模拟控制面不可达")
        }
        val url = request.url
        return when {
            url.contains("/schedule/candidates") -> {
                candidatesCalls.incrementAndGet()
                HttpResponse(candidatesStatus, BODY_CANDIDATES)
            }

            url.contains("/schedule/decide") -> {
                decideCalls.incrementAndGet()
                HttpResponse(decideStatus, BODY_DECIDE)
            }

            url.contains("/schedule/report-local") -> {
                reportCalls.incrementAndGet()
                HttpResponse(reportStatus, BODY_REPORT)
            }

            else -> HttpResponse(404, "")
        }
    }

    companion object {
        const val BODY_CANDIDATES = "candidates-ok"
        const val BODY_DECIDE = "decide-ok"
        const val BODY_REPORT = "report-ok"
    }
}

/** 极简 codec：encode 返回占位，decode 按 body key 返回预置泛型树。 */
class SchedCannedCodec : JsonCodec {
    override fun encode(value: Any?): String = "encoded"

    override fun decode(json: String): Any? =
        when (json) {
            SchedFakeTransport.BODY_CANDIDATES ->
                mapOf(
                    "generatedAtMs" to 1_000L,
                    "zones" to
                        listOf(
                            mapOf(
                                "zone" to "z-a",
                                "candidates" to
                                    listOf(
                                        candidateTree(candidateEntry("lobby-1", 90, "healthy", true, 3, 100)),
                                        candidateTree(candidateEntry("lobby-2", 70, "degraded", true, 8, 100)),
                                    ),
                            ),
                        ),
                )

            SchedFakeTransport.BODY_DECIDE ->
                mapOf(
                    "traceId" to "srv-trace-1",
                    "chosen" to mapOf("serverId" to "lobby-1", "score" to 90),
                    "candidateCount" to 2,
                    "excludedCount" to 0,
                )

            SchedFakeTransport.BODY_REPORT -> mapOf("accepted" to 1, "deduplicated" to 0)

            else -> emptyMap<String, Any?>()
        }

    private fun candidateTree(entry: CandidateEntry): Map<String, Any?> =
        mapOf(
            "serverId" to entry.serverId,
            "score" to entry.score,
            "level" to entry.level,
            "schedulable" to entry.schedulable,
            "onlineCount" to entry.onlineCount,
            "maxOnline" to entry.maxOnline,
        )
}

/** 手动调度适配器：runAsync 同步执行，runAsyncDelayed 只入队（测试显式推进）；捕获各级日志供 warn-once 断言。 */
class ManualSchedAdapter(private val folder: File) : PlatformAdapter {
    val delayed = ArrayDeque<() -> Unit>()
    val infos = mutableListOf<String>()
    val warns = mutableListOf<String>()

    override fun runAsync(task: () -> Unit) {
        task()
    }

    override fun runAsyncDelayed(
        delayMs: Long,
        task: () -> Unit,
    ) {
        delayed.addLast(task)
    }

    override fun runSync(task: () -> Unit) {
        task()
    }

    override fun dataFolder(): File = folder

    override fun publishConfigChanged(
        changed: Set<String>,
        newMd5: String,
    ) {}

    override fun info(msg: String) {
        infos.add(msg)
    }

    override fun warn(msg: String) {
        warns.add(msg)
    }

    override fun error(
        msg: String,
        t: Throwable?,
    ) {}

    /** 推进一个延迟任务（下一次刷新 tick）。 */
    fun drainOne() {
        delayed.removeFirst().invoke()
    }
}

/**
 * 往返 codec：encode 把泛型树存入内存表并返回 token，decode 按 token（经文件文本往返后不变）取回原树。
 * 用于验证 SchedulingSnapshotStore 的「build tree → 落盘 → 读盘 → parse tree」自洽（不依赖真实 JSON 实现）。
 */
class RoundTripCodec : JsonCodec {
    private val store = java.util.concurrent.ConcurrentHashMap<String, Any?>()
    private val seq = AtomicInteger(0)

    override fun encode(value: Any?): String {
        val token = "tree-${seq.incrementAndGet()}"
        store[token] = value
        return token
    }

    override fun decode(json: String): Any? = store[json.trim()] ?: emptyMap<String, Any?>()
}

/** 构造候选条目（测试夹具）。 */
fun candidateEntry(
    serverId: String,
    score: Int,
    level: String = "healthy",
    schedulable: Boolean = true,
    online: Int = 0,
    max: Int = 100,
): CandidateEntry = CandidateEntry(serverId, score, level, schedulable, online, max)

/** 构造候选快照（单 zone 便捷）。 */
fun snapshotOf(
    zone: String,
    candidates: List<CandidateEntry>,
    generatedAtMs: Long = 1_000L,
    savedAtMs: Long = 1_000L,
): CandidateSnapshot = CandidateSnapshot(generatedAtMs, savedAtMs, linkedMapOf(zone to candidates))

/** 测试用 agent 身份（带 v2 身份，供鉴权头注入）。 */
fun schedIdentity(): AgentIdentity =
    AgentIdentity(
        namespace = "prod",
        serverId = "lobby-1",
        role = "bukkit",
        groupHint = "area1",
        address = "127.0.0.1:25565",
        version = "1.0",
        capacity = 100,
        weight = 1,
        metadata = emptyMap(),
        identityId = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        bootId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
    )

/** 测试用 agent 设置。 */
fun schedSettings(): AgentSettings =
    AgentSettings(
        endpoints = listOf("http://localhost:8848"),
        bootstrapToken = "tk",
        pollTimeoutMs = 50,
        requestTimeoutMs = 200,
        heartbeatFallbackMs = 100_000,
        backoff = BackoffSettings(initialMs = 1000, maxMs = 1000, multiplier = 1.0, jitterRatio = 0.0),
        snapshotEnabled = true,
        snapshotFileName = "snapshot.json",
        fileTree = FileTreeSettings(enabled = false, targetSubDir = "", appliedManifestFileName = "file-tree.applied.json"),
        override = OverrideSettings(commandWhitelist = emptySet(), backupDirName = "override-backup"),
    )
