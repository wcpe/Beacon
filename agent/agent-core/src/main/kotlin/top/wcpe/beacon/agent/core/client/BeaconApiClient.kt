package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.config.ConfigItem
import top.wcpe.beacon.agent.core.config.EffectiveResult
import top.wcpe.beacon.agent.core.filetree.FileContent
import top.wcpe.beacon.agent.core.filetree.FileManifest
import top.wcpe.beacon.agent.core.filetree.FileManifestEntry
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.metrics.ProxyMetrics
import top.wcpe.beacon.agent.core.override.OverrideManifest
import top.wcpe.beacon.agent.core.override.OverrideSetEntry
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import top.wcpe.beacon.agent.core.transport.StreamListener
import top.wcpe.beacon.agent.core.transport.StreamRequest
import top.wcpe.beacon.agent.core.transport.StreamTransport

/**
 * 收口 agent REST 语义调用：register / heartbeat / pollEffective / report / discover，
 * 以及文件树托管（通道B）的 pollFileManifest / fetchFileContent。
 *
 * 用 Map<String,Any?> 拼请求体经 codec.encode；响应 codec.decode 成泛型树后映射到 core 数据类。
 * core 内不出现 @Serializable 类型（ADR-0005）。
 */
class BeaconApiClient(
    private val transport: HttpTransport,
    private val codec: JsonCodec,
    private val settings: AgentSettings,
    // 流式传输（SSE 推送，FR-24）：可选；为 null 时退回三条长轮询（迁移期兼容，见 ADR-0015 决策 8）。
    private val streamTransport: StreamTransport? = null,
) {

    private val base: String = settings.primaryEndpoint()

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
    private val lastConnectFailure: ThreadLocal<String?> = ThreadLocal()

    /** 当前是否具备 SSE 推送能力（注入了 streamTransport）。 */
    fun streamingEnabled(): Boolean = streamTransport != null

    /**
     * 打开 server→agent 单条 SSE 推送流：GET /beacon/v1/agent/stream（FR-24）。
     *
     * URL 携带各通道当前 md5 供控制面"连接即对账"补发落下的增量；同步阻塞直到流结束。
     * 仅在异步线程调用（绝不上 MC 主线程）；未注入 streamTransport 时直接回调 onClosed。
     */
    fun openStream(identity: AgentIdentity, reported: ReportedChannelMd5, listener: StreamListener) {
        val st = streamTransport
        if (st == null) {
            listener.onClosed(IllegalStateException("未注入 streamTransport"))
            return
        }
        val url = buildString {
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
            StreamRequest(url = url, headers = headers(withBody = false), readTimeoutMs = readTimeout),
            listener,
        )
    }

    /** agent 侧公共请求头：内容类型 + 防误连 token。 */
    private fun headers(withBody: Boolean): Map<String, String> {
        val h = LinkedHashMap<String, String>()
        h[HEADER_TOKEN] = settings.bootstrapToken
        if (withBody) {
            h["Content-Type"] = "application/json; charset=utf-8"
        }
        return h
    }

    /**
     * 注册：POST /beacon/v1/agent/register。
     *
     * backends 为本机（仅 bc 代理）当前代理的后端子服 serverId 集合（FR-36 事实，非身份），
     * 由调用方按帧传入；仅当非空时才拼入报文（bukkit / 旧控制面下为空、不拼，向后兼容）。
     */
    fun register(identity: AgentIdentity, backends: List<String> = emptyList()): RegisterOutcome {
        val body = buildMap {
            put("namespace", identity.namespace)
            put("serverId", identity.serverId)
            put("role", identity.role)
            put("groupHint", identity.groupHint)
            put("address", identity.address)
            put("version", identity.version)
            put("capacity", identity.capacity)
            put("weight", identity.weight)
            put("metadata", identity.metadata)
            // bc 后端归属事实：仅非空时附加，旧控制面忽略即可（FR-36）。
            if (backends.isNotEmpty()) put("backends", backends)
        }
        val resp = exec(
            HttpRequest(
                method = "POST",
                url = "$base/beacon/v1/agent/register",
                headers = headers(withBody = true),
                body = codec.encode(body),
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return RegisterOutcome.Failed(connectFailReason())

        return when (resp.statusCode) {
            200 -> RegisterOutcome.Success(parseRegister(resp.body))
            409 -> RegisterOutcome.DuplicateServerId
            401 -> RegisterOutcome.Unauthorized
            400 -> RegisterOutcome.IdentityRequired
            else -> RegisterOutcome.Failed("非预期状态码 ${resp.statusCode}")
        }
    }

    /** 心跳：POST /beacon/v1/agent/heartbeat。404 → 需重新注册。 */
    fun heartbeat(identity: AgentIdentity): HeartbeatOutcome {
        val body = mapOf(
            "namespace" to identity.namespace,
            "serverId" to identity.serverId,
        )
        val resp = exec(
            HttpRequest(
                method = "POST",
                url = "$base/beacon/v1/agent/heartbeat",
                headers = headers(withBody = true),
                body = codec.encode(body),
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return HeartbeatOutcome.Failed(connectFailReason())

        return when (resp.statusCode) {
            200 -> {
                val obj = JsonTree.asObject(codec.decode(resp.body))
                HeartbeatOutcome.Ok(
                    HeartbeatResult(
                        ttlSec = JsonTree.intOr(obj, "ttlSec", 0),
                        configDirty = JsonTree.boolOr(obj, "configDirty", false),
                    ),
                )
            }

            404 -> HeartbeatOutcome.NotRegistered
            else -> HeartbeatOutcome.Failed("非预期状态码 ${resp.statusCode}")
        }
    }

    /** 长轮询有效配置：GET /beacon/v1/agent/config/effective。 */
    fun pollEffective(identity: AgentIdentity, currentMd5: String?, timeoutMs: Long): PollResult {
        val md5Param = currentMd5 ?: ""
        val url = buildString {
            append(base)
            append("/beacon/v1/agent/config/effective")
            append("?namespace=").append(urlEncode(identity.namespace))
            append("&serverId=").append(urlEncode(identity.serverId))
            append("&md5=").append(urlEncode(md5Param))
            append("&timeoutMs=").append(timeoutMs)
        }
        // 读超时给长轮询留余量（挂起上限 + 普通读超时）。
        val resp = exec(
            HttpRequest(
                method = "GET",
                url = url,
                headers = headers(withBody = false),
                body = null,
                readTimeoutMs = timeoutMs + settings.requestTimeoutMs,
            ),
        ) ?: return PollResult.Failed(connectFailReason())

        return when (resp.statusCode) {
            200 -> PollResult.Changed(parseEffective(resp.body))
            304 -> PollResult.NotModified
            404 -> PollResult.NotRegistered
            else -> PollResult.Failed("非预期状态码 ${resp.statusCode}")
        }
    }

    /**
     * 上报状态：POST /beacon/v1/agent/report。
     *
     * playerCount / tps / memUsed / memMax / cpuLoad 均为「负载数字（健康事实）」，仅供控制面看板展示、
     * 不参与调度决策（FR-32 / ADR-0023）。新增 memUsed/memMax/cpuLoad 三键为附加字段，
     * 旧控制面忽略即可，向后兼容。cpuLoad 取不到时上报 -1.0（不可用），由控制面判定。
     *
     * backends 为本机（仅 bc 代理）当前代理的后端子服 serverId 集合（FR-36 事实），随上报刷新；
     * 仅当非空时才拼入报文（bukkit / 旧控制面下为空、不拼，向后兼容）。
     *
     * proxy 为 bc 专属负载指标（连接 / 线程 / 运行时长 / 后端可达性·延迟，FR-34）；仅 bc 壳注入，
     * 仅当非 null 时才拼入 `proxy` 子对象（bukkit / 旧控制面下为 null、不拼，向后兼容）。
     */
    fun report(
        identity: AgentIdentity,
        appliedMd5: String,
        playerCount: Int,
        tps: Double,
        memUsed: Long,
        memMax: Long,
        cpuLoad: Double,
        backends: List<String> = emptyList(),
        proxy: ProxyMetrics? = null,
    ): Boolean {
        val body = buildMap {
            put("namespace", identity.namespace)
            put("serverId", identity.serverId)
            put("appliedMd5", appliedMd5)
            put("playerCount", playerCount)
            put("tps", tps)
            // 新增：JVM 已用 / 最大堆字节与进程 CPU 负载（键名固定供控制面对齐）。
            put("memUsed", memUsed)
            put("memMax", memMax)
            put("cpuLoad", cpuLoad)
            // bc 后端归属事实：仅非空时附加，旧控制面忽略即可（FR-36）。
            if (backends.isNotEmpty()) put("backends", backends)
            // bc 专属负载指标：仅 bc 壳注入时附加 proxy 子对象，旧控制面忽略即可（FR-34）。
            if (proxy != null) put("proxy", proxyBody(proxy))
        }
        val resp = exec(
            HttpRequest(
                method = "POST",
                url = "$base/beacon/v1/agent/report",
                headers = headers(withBody = true),
                body = codec.encode(body),
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return false
        return resp.statusCode == 200
    }

    /**
     * 服务发现：GET /beacon/v1/agent/discovery。同步调用，请在异步线程使用。
     *
     * 传 null 的过滤维度不拼入查询；tags 以 tag.<key>=<value> 形式拼入（多 tag 取交集，FR-29）。
     * 返回可用实例（online+degraded）的泛型树列表（由调用方映射为 API 值对象）。
     */
    fun discover(
        namespace: String?,
        group: String?,
        zone: String?,
        role: String?,
        tags: Map<String, String> = emptyMap(),
    ): List<Map<String, Any?>> {
        val params = StringBuilder()
        appendParam(params, "namespace", namespace)
        appendParam(params, "group", group)
        appendParam(params, "zone", zone)
        appendParam(params, "role", role)
        // 自定义元数据过滤：每个键拼为 tag.<key>=<value>，控制面按 metadata 键值匹配。
        for ((k, v) in tags) {
            appendParam(params, "tag.$k", v)
        }
        val url = "$base/beacon/v1/agent/discovery" + if (params.isEmpty()) "" else "?$params"

        val resp = exec(
            HttpRequest(
                method = "GET",
                url = url,
                headers = headers(withBody = false),
                body = null,
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return emptyList()
        if (resp.statusCode != 200) {
            return emptyList()
        }
        val obj = JsonTree.asObject(codec.decode(resp.body))
        return JsonTree.asList(obj["instances"]).map { JsonTree.asObject(it) }
    }

    /**
     * 长轮询文件清单：GET /beacon/v1/agent/files/manifest（通道B）。
     *
     * 带当前 fileTreeMd5；变了 200 返回新清单（path→md5，不含内容），未变到超时 304。
     * 与配置长轮询唤醒集合独立（见 ADR-0010）。
     */
    fun pollFileManifest(identity: AgentIdentity, currentMd5: String?, timeoutMs: Long): FileManifestPollResult {
        val md5Param = currentMd5 ?: ""
        val url = buildString {
            append(base)
            append("/beacon/v1/agent/files/manifest")
            append("?namespace=").append(urlEncode(identity.namespace))
            append("&serverId=").append(urlEncode(identity.serverId))
            append("&md5=").append(urlEncode(md5Param))
            append("&timeoutMs=").append(timeoutMs)
        }
        // 读超时给长轮询留余量（挂起上限 + 普通读超时）。
        val resp = exec(
            HttpRequest(
                method = "GET",
                url = url,
                headers = headers(withBody = false),
                body = null,
                readTimeoutMs = timeoutMs + settings.requestTimeoutMs,
            ),
        ) ?: return FileManifestPollResult.Failed(connectFailReason())

        return when (resp.statusCode) {
            200 -> FileManifestPollResult.Changed(parseManifest(resp.body))
            304 -> FileManifestPollResult.NotModified
            404 -> FileManifestPollResult.NotRegistered
            else -> FileManifestPollResult.Failed("非预期状态码 ${resp.statusCode}")
        }
    }

    /**
     * 取单个文件内容：GET /beacon/v1/agent/files/content（通道B）。同步调用，请在异步线程使用。
     *
     * 200 返回该 path 按覆盖链解析后的整文件内容；404（FILE_NOT_FOUND/未注册）或连接失败返回 null。
     */
    fun fetchFileContent(identity: AgentIdentity, path: String): FileContent? {
        val url = buildString {
            append(base)
            append("/beacon/v1/agent/files/content")
            append("?namespace=").append(urlEncode(identity.namespace))
            append("&serverId=").append(urlEncode(identity.serverId))
            append("&path=").append(urlEncode(path))
        }
        val resp = exec(
            HttpRequest(
                method = "GET",
                url = url,
                headers = headers(withBody = false),
                body = null,
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return null
        if (resp.statusCode != 200) {
            return null
        }
        val obj = JsonTree.asObject(codec.decode(resp.body))
        return FileContent(
            path = JsonTree.strOr(obj, "path", ""),
            md5 = JsonTree.strOr(obj, "md5", ""),
            content = JsonTree.strOr(obj, "content", ""),
        )
    }

    /**
     * 长轮询三方覆盖集投递：GET /beacon/v1/agent/override-sets（FR-15）。
     *
     * 带当前 overrideMd5；变了 200 返回适用覆盖集（目标根 + 命令 + 成员 path，不含内容），未变到超时 304。
     * 与文件长轮询复用同一唤醒集合（同属通道B），但 md5 维度独立（见 ADR-0011）。
     */
    fun pollOverrideSets(identity: AgentIdentity, currentMd5: String?, timeoutMs: Long): OverridePollResult {
        val md5Param = currentMd5 ?: ""
        val url = buildString {
            append(base)
            append("/beacon/v1/agent/override-sets")
            append("?namespace=").append(urlEncode(identity.namespace))
            append("&serverId=").append(urlEncode(identity.serverId))
            append("&md5=").append(urlEncode(md5Param))
            append("&timeoutMs=").append(timeoutMs)
        }
        val resp = exec(
            HttpRequest(
                method = "GET",
                url = url,
                headers = headers(withBody = false),
                body = null,
                readTimeoutMs = timeoutMs + settings.requestTimeoutMs,
            ),
        ) ?: return OverridePollResult.Failed(connectFailReason())

        return when (resp.statusCode) {
            200 -> OverridePollResult.Changed(parseOverrideManifest(resp.body))
            304 -> OverridePollResult.NotModified
            404 -> OverridePollResult.NotRegistered
            else -> OverridePollResult.Failed("非预期状态码 ${resp.statusCode}")
        }
    }

    /**
     * 取某覆盖集成员文件内容：GET /beacon/v1/agent/override-sets/content（FR-15）。同步调用，请在异步线程使用。
     *
     * 200 返回该 (set, path) 按覆盖链解析后的整文件内容；404 或连接失败返回 null（触发 fail-static 放弃本轮）。
     */
    fun fetchOverrideMember(identity: AgentIdentity, setName: String, path: String): FileContent? {
        val url = buildString {
            append(base)
            append("/beacon/v1/agent/override-sets/content")
            append("?namespace=").append(urlEncode(identity.namespace))
            append("&serverId=").append(urlEncode(identity.serverId))
            append("&set=").append(urlEncode(setName))
            append("&path=").append(urlEncode(path))
        }
        val resp = exec(
            HttpRequest(
                method = "GET",
                url = url,
                headers = headers(withBody = false),
                body = null,
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return null
        if (resp.statusCode != 200) {
            return null
        }
        val obj = JsonTree.asObject(codec.decode(resp.body))
        return FileContent(
            path = JsonTree.strOr(obj, "path", ""),
            md5 = JsonTree.strOr(obj, "md5", ""),
            content = JsonTree.strOr(obj, "content", ""),
        )
    }

    /**
     * 执行请求；连接级异常统一吞为 null（由上层转 Failed/退避）。
     *
     * 吞异常前把"类名 + 消息"记入 [lastConnectFailure]，调用方可经 [connectFailReason]
     * 把它带进 Failed.reason，避免诊断完全黑盒（在此处不再额外打日志，由上层一处统一 WARN）。
     */
    private fun exec(request: HttpRequest): HttpResponse? {
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
    private fun connectFailReason(): String = lastConnectFailure.get() ?: "连接失败"

    private fun parseRegister(jsonBody: String): RegisterResult {
        val obj = JsonTree.asObject(codec.decode(jsonBody))
        return RegisterResult(
            instanceKey = JsonTree.strOr(obj, "instanceKey", ""),
            resolvedGroup = JsonTree.str(obj, "resolvedGroup"),
            resolvedZone = JsonTree.str(obj, "resolvedZone"),
            heartbeatIntervalSec = JsonTree.intOr(obj, "heartbeatIntervalSec", 0),
            ttlSec = JsonTree.intOr(obj, "ttlSec", 0),
            assigned = JsonTree.boolOr(obj, "assigned", false),
        )
    }

    private fun parseEffective(jsonBody: String): EffectiveResult {
        val obj = JsonTree.asObject(codec.decode(jsonBody))
        val items = JsonTree.asList(obj["items"]).map { raw ->
            val itemObj = JsonTree.asObject(raw)
            ConfigItem(
                dataId = JsonTree.strOr(itemObj, "dataId", ""),
                format = JsonTree.strOr(itemObj, "format", ""),
                md5 = JsonTree.strOr(itemObj, "md5", ""),
                content = JsonTree.strOr(itemObj, "content", ""),
            )
        }
        return EffectiveResult(
            namespace = JsonTree.strOr(obj, "namespace", ""),
            serverId = JsonTree.strOr(obj, "serverId", ""),
            group = JsonTree.str(obj, "group"),
            zone = JsonTree.str(obj, "zone"),
            md5 = JsonTree.strOr(obj, "md5", ""),
            items = items,
        )
    }

    private fun parseManifest(jsonBody: String): FileManifest {
        val obj = JsonTree.asObject(codec.decode(jsonBody))
        val entries = JsonTree.asList(obj["files"]).map { raw ->
            val fileObj = JsonTree.asObject(raw)
            FileManifestEntry(
                path = JsonTree.strOr(fileObj, "path", ""),
                md5 = JsonTree.strOr(fileObj, "md5", ""),
            )
        }
        return FileManifest(
            namespace = JsonTree.strOr(obj, "namespace", ""),
            serverId = JsonTree.strOr(obj, "serverId", ""),
            group = JsonTree.str(obj, "group"),
            zone = JsonTree.str(obj, "zone"),
            fileTreeMd5 = JsonTree.strOr(obj, "fileTreeMd5", ""),
            entries = entries,
        )
    }

    private fun parseOverrideManifest(jsonBody: String): OverrideManifest {
        val obj = JsonTree.asObject(codec.decode(jsonBody))
        val sets = JsonTree.asList(obj["sets"]).map { raw ->
            val setObj = JsonTree.asObject(raw)
            OverrideSetEntry(
                name = JsonTree.strOr(setObj, "name", ""),
                targetRoot = JsonTree.strOr(setObj, "targetRoot", ""),
                // 空命令在控制面投递为 ""；归一化为 null（不下发命令），与 OverrideApplier 入参语义一致。
                reloadCommand = JsonTree.strOr(setObj, "reloadCommand", "").ifEmpty { null },
                members = JsonTree.asList(setObj["members"]).map { m -> JsonTree.asString(m) },
            )
        }
        return OverrideManifest(
            namespace = JsonTree.strOr(obj, "namespace", ""),
            serverId = JsonTree.strOr(obj, "serverId", ""),
            overrideMd5 = JsonTree.strOr(obj, "overrideMd5", ""),
            sets = sets,
        )
    }

    /** 把 BC 专属指标拼成 report 报文的 `proxy` 子对象（键名固定供控制面对齐，FR-34）。 */
    private fun proxyBody(proxy: ProxyMetrics): Map<String, Any?> = mapOf(
        "onlineConnections" to proxy.onlineConnections,
        "threadCount" to proxy.threadCount,
        "uptimeMs" to proxy.uptimeMs,
        "backendUp" to proxy.backendUp,
        "backendTotal" to proxy.backendTotal,
        "backendAvgLatencyMs" to proxy.backendAvgLatencyMs,
    )

    private fun appendParam(sb: StringBuilder, key: String, value: String?) {
        if (value.isNullOrEmpty()) return
        if (sb.isNotEmpty()) sb.append('&')
        sb.append(key).append('=').append(urlEncode(value))
    }

    private fun urlEncode(value: String): String {
        return java.net.URLEncoder.encode(value, Charsets.UTF_8.name())
    }

    companion object {
        /** agent 侧防误连令牌请求头名。 */
        const val HEADER_TOKEN: String = "X-Beacon-Token"
    }
}

/** 心跳结果分类。 */
sealed class HeartbeatOutcome {

    /** 200：心跳成功。 */
    data class Ok(val result: HeartbeatResult) : HeartbeatOutcome()

    /** 404：未注册，需重新注册。 */
    object NotRegistered : HeartbeatOutcome()

    /** 连接级失败/其它非预期状态。 */
    data class Failed(val reason: String) : HeartbeatOutcome()
}
