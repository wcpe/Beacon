package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import top.wcpe.beacon.agent.core.transport.StreamListener
import top.wcpe.beacon.agent.core.transport.StreamRequest
import top.wcpe.beacon.agent.core.transport.StreamTransport

data class DiscoveryFilters(
    val namespace: String?,
    val group: String?,
    val zone: String?,
    val role: String?,
    val tags: Map<String, String> = emptyMap(),
)

/** 上报负载数字（健康事实）：人数 / TPS / JVM 堆 / CPU 负载，仅供控制面看板展示、不参与调度决策（FR-32 / ADR-0023）。 */
data class HealthMetrics(
    val playerCount: Int,
    val tps: Double,
    val memUsed: Long,
    val memMax: Long,
    val cpuLoad: Double,
)

/**
 * 收口 agent REST 语义调用：register / heartbeat / pollEffective / report / discover，
 * 以及文件树托管（通道B）的 pollFileManifest / fetchFileContent。
 *
 * 用 Map<String,Any?> 拼请求体经 codec.encode；响应 codec.decode 成泛型树后映射到 core 数据类。
 * core 内不出现 @Serializable 类型（ADR-0005）。
 */
class BeaconApiClient(
    internal val transport: HttpTransport,
    internal val codec: JsonCodec,
    internal val settings: AgentSettings,
    // 流式传输（SSE 推送，FR-24）：可选；为 null 时退回三条长轮询（迁移期兼容，见 ADR-0015 决策 8）。
    internal val streamTransport: StreamTransport? = null,
) {
    internal val base: String = settings.primaryEndpoint()

    /**
     * 上一次 [exec] 因连接级异常被吞掉的具体原因（类名 + 消息），per-thread。
     *
     * 仅作诊断用：当 [exec] 返回 null 时由 **同一线程内** 的调用方读取以构造 Failed.reason，
     * 让外层日志能看清是 ConnectException / SocketTimeoutException 还是其他——而非笼统的"连接失败"。
     *
     * 并发模型：BeaconApiClient 是单例，被 AgentLifecycle 的 5 条独立异步循环共用
     * （register / heartbeat / pollEffective / pollFileManifest / pollOverride），它们彼此并发。
     * 若用共享 @Volatile 字段，一条循环的"成功后置 null"会抹掉另一条循环刚记下的失败原因，
     * 致 caller 拿到 null 兜底回笼统文案；或一条循环的失败覆盖另一条的失败。
     * 改用 [ThreadLocal] 后，reason 仅在抛异常的线程内可见，写读必落同一线程，根除跨循环串台。
     */
    internal val lastConnectFailure: ThreadLocal<String?> = ThreadLocal()

    /** 当前是否具备 SSE 推送能力（注入了 streamTransport）。 */
    fun streamingEnabled(): Boolean = streamTransport != null

    /**
     * 打开 server→agent 单条 SSE 推送流：GET /beacon/v1/agent/stream（FR-24）。
     *
     * URL 携带各通道当前 md5 供控制面"连接即对账"补发落下的增量；同步阻塞直到流结束。
     * 仅在异步线程调用（绝不上 MC 主线程）；未注入 streamTransport 时直接回调 onClosed。
     */
    fun openStream(
        identity: AgentIdentity,
        reported: ReportedChannelMd5,
        listener: StreamListener,
    ) {
        val st = streamTransport
        if (st == null) {
            listener.onClosed(IllegalStateException("未注入 streamTransport"))
            return
        }
        val url =
            buildString {
                append(base)
                append("/beacon/v1/agent/stream")
                append("?namespace=").append(urlEncode(identity.namespace))
                append("&serverId=").append(urlEncode(identity.serverId))
                append("&configMd5=").append(urlEncode(reported.config))
                append("&fileMd5=").append(urlEncode(reported.file))
                append("&overrideMd5=").append(urlEncode(reported.override))
                append("&topologyMd5=").append(urlEncode(reported.topology))
            }
        // 读超时给保活留充足余量：取长轮询挂起上限的数倍，避免空闲被误判断流。
        val readTimeout = settings.pollTimeoutMs * 3 + settings.requestTimeoutMs
        st.open(
            StreamRequest(url = url, headers = headers(withBody = false, identity = identity), readTimeoutMs = readTimeout),
            listener,
        )
    }

    /** agent 侧公共请求头：内容类型 + 防误连 token。 */
    internal fun headers(
        withBody: Boolean,
        identity: AgentIdentity? = null,
    ): Map<String, String> {
        val h = LinkedHashMap<String, String>()
        h[HEADER_TOKEN] = settings.bootstrapToken
        if (identity?.hasV2Identity() == true) {
            h[HEADER_IDENTITY] = identity.identityId
            h[HEADER_BOOT] = identity.bootId
        }
        if (withBody) {
            h["Content-Type"] = "application/json; charset=utf-8"
        }
        return h
    }

    /**
     * agent 面鉴权头（X-Beacon-Token + v2 X-Beacon-Identity / X-Beacon-Boot），供交付流式数据面
     * （[top.wcpe.beacon.agent.core.transport.BlobStreamTransport]）复用同一鉴权真源（FR-165，见 ADR-0069）。
     *
     * 不含 Content-Type：blob PUT 的内容类型由流式适配器按 octet-stream 设置，本头只承载鉴权。
     */
    fun agentAuthHeaders(identity: AgentIdentity): Map<String, String> = headers(withBody = false, identity = identity)

    /**
     * 执行请求；连接级异常统一吞为 null（由上层转 Failed/退避）。
     *
     * 吞异常前把"类名 + 消息"记入 [lastConnectFailure]，调用方可经 [connectFailReason]
     * 把它带进 Failed.reason，避免诊断完全黑盒（在此处不再额外打日志，由上层一处统一 WARN）。
     */
    internal fun exec(request: HttpRequest): HttpResponse? {
        return try {
            val resp = transport.execute(request)
            // 成功路径清理本线程的 reason，避免后续无关请求误把陈旧失败带回诊断（仍只影响本线程，不跨循环）。
            lastConnectFailure.set(null)
            resp
        } catch (e: Exception) {
            lastConnectFailure.set("${e.javaClass.simpleName}: ${e.message ?: "无错误信息"}")
            null
        }
    }

    /** 取上一次连接失败的具体原因（本线程内）；从未失败则回退到笼统文案。 */
    internal fun connectFailReason(): String = lastConnectFailure.get() ?: "连接失败"

    /** 解析 self 健康段（FR-147/FR-148）；缺失 / 非对象（含 JSON null）返回 null。 */
    internal fun parseSelfHealth(raw: Any?): SelfHealth? {
        if (raw !is Map<*, *>) return null
        val obj = JsonTree.asObject(raw)
        return SelfHealth(
            score = JsonTree.intOr(obj, "score", 0),
            level = JsonTree.strOr(obj, "level", ""),
            schedulable = JsonTree.boolOr(obj, "schedulable", false),
            reasons = JsonTree.asList(obj["reasons"]).map { JsonTree.asString(it) },
        )
    }

    /** 从错误响应体解析原因码（优先 code，退 message，再退笼统）；供 400 拒绝告警可读（已脱敏，ADR-0057）。 */
    internal fun parseErrorCode(jsonBody: String): String {
        val obj = JsonTree.asObject(codec.decode(jsonBody))
        return JsonTree.str(obj, "code") ?: JsonTree.str(obj, "message") ?: "请求被拒"
    }

    internal fun v2IdentityHeaders(identity: AgentIdentity): Map<String, String> {
        val h = LinkedHashMap<String, String>()
        h[HEADER_TOKEN] = settings.bootstrapToken
        h[HEADER_IDENTITY] = identity.identityId
        h[HEADER_BOOT] = identity.bootId
        return h
    }

    companion object {
        internal val FINGERPRINT = Regex("[0-9a-f]{64}")
        internal const val DISCOVERY_REQUEST_FAILED = "发现请求失败"
        internal const val DISCOVERY_DECODE_FAILED = "发现响应解码失败"
        internal const val DISCOVERY_STRUCTURE_INVALID = "发现响应结构无效"

        /**
         * decide 决策读超时（毫秒，FR-148 §4.6 / §8 待定 13）：玩家链路不容久等，超时即由上层降级本地决策。
         * 工程默认值，非契约。
         */
        const val SCHED_DECIDE_TIMEOUT_MS: Long = 800L

        /** agent 侧防误连令牌请求头名。 */
        const val HEADER_TOKEN: String = "X-Beacon-Token"

        /** v2 已绑定身份请求头名。 */
        const val HEADER_IDENTITY: String = "X-Beacon-Identity"

        /** v2 本次进程启动标识请求头名。 */
        const val HEADER_BOOT: String = "X-Beacon-Boot"
    }
}

/** 心跳结果分类。 */
sealed class HeartbeatOutcome {
    /** 200：心跳成功。 */
    data class Ok(val result: HeartbeatResult) : HeartbeatOutcome()

    /** 404：未注册，需重新注册。 */
    object NotRegistered : HeartbeatOutcome()

    /** v1 数据面拒绝，必须回查 v2 权威身份状态。 */
    object AuthorityRefreshRequired : HeartbeatOutcome()

    /** 连接级失败/其它非预期状态。 */
    data class Failed(val reason: String) : HeartbeatOutcome()
}

// ===== 顶层工具函数（从 BeaconApiClient 提取，不占类方法数） =====

// ===== BeaconApiClient 扩展函数（从类内提取，不占类方法数） =====

// ===== 顶层工具函数（从 BeaconApiClient 提取，不占类方法数） =====

// ===== BeaconApiClient 扩展函数（从类内提取，不占类方法数） =====
