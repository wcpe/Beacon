package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.browse.AssetContent
import top.wcpe.beacon.agent.core.command.AgentCommand
import top.wcpe.beacon.agent.core.command.AssetEntry
import top.wcpe.beacon.agent.core.command.DeliveryCommandPayload
import top.wcpe.beacon.agent.core.command.IngestCommandPayload
import top.wcpe.beacon.agent.core.command.IngestFile
import top.wcpe.beacon.agent.core.command.ScanFile
import top.wcpe.beacon.agent.core.config.ConfigItem
import top.wcpe.beacon.agent.core.config.EffectiveResult
import top.wcpe.beacon.agent.core.connection.ConnectionEvent
import top.wcpe.beacon.agent.core.delivery.DeliveryManifestFile
import top.wcpe.beacon.agent.core.delivery.DeliveryStageReport
import top.wcpe.beacon.agent.core.delivery.DeliveryTargetManifest
import top.wcpe.beacon.agent.core.delivery.DeliveryUploadItem
import top.wcpe.beacon.agent.core.delivery.DeliveryUploadManifest
import top.wcpe.beacon.agent.core.filetree.FileContent
import top.wcpe.beacon.agent.core.filetree.FileManifest
import top.wcpe.beacon.agent.core.filetree.FileManifestEntry
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.log.LogLine
import top.wcpe.beacon.agent.core.metrics.ProxyMetrics
import top.wcpe.beacon.agent.core.override.OverrideManifest
import top.wcpe.beacon.agent.core.override.OverrideSetEntry
import top.wcpe.beacon.agent.core.sampling.BackendBatch
import top.wcpe.beacon.agent.core.sampling.MetricBatch
import top.wcpe.beacon.agent.core.sampling.MetricKind
import top.wcpe.beacon.agent.core.sampling.MetricSample
import top.wcpe.beacon.agent.core.sampling.ProxyBatch
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
    private fun headers(
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
     * 注册：POST /beacon/v1/agent/register。
     *
     * backends 为本机（仅 bc 代理）当前代理的后端子服 serverId 集合（FR-36 事实，非身份），
     * 由调用方按帧传入；仅当非空时才拼入报文（bukkit / 旧控制面下为空、不拼，向后兼容）。
     */
    fun register(
        identity: AgentIdentity,
        backends: List<String> = emptyList(),
    ): RegisterOutcome {
        if (identity.hasV2Identity()) {
            return registerV2(identity, backends)
        }
        return registerLegacy(identity, backends)
    }

    private fun registerLegacy(
        identity: AgentIdentity,
        backends: List<String> = emptyList(),
    ): RegisterOutcome {
        val body =
            buildMap {
                put("namespace", identity.namespace)
                put("serverId", identity.serverId)
                put("role", identity.role)
                put("groupHint", identity.groupHint)
                put("address", identity.address)
                put("version", identity.version)
                put("capacity", identity.capacity)
                put("weight", identity.weight)
                put("metadata", identity.metadata)
                // agent 自身构建版本：仅非空时附加，旧控制面 / 旧 agent 缺键即可（FR-86，见 ADR-0039）。
                if (identity.agentVersion.isNotBlank()) put("agentVersion", identity.agentVersion)
                // bc 后端归属事实：仅非空时附加，旧控制面忽略即可（FR-36）。
                if (backends.isNotEmpty()) put("backends", backends)
            }
        val resp =
            exec(
                HttpRequest(
                    method = "POST",
                    url = "$base/beacon/v1/agent/register",
                    headers = headers(withBody = true, identity = identity),
                    body = codec.encode(body),
                    readTimeoutMs = settings.requestTimeoutMs,
                ),
            ) ?: return RegisterOutcome.Failed(connectFailReason())

        return when (resp.statusCode) {
            200 -> RegisterOutcome.Success(parseRegister(resp.body))
            409 -> RegisterOutcome.DuplicateServerId
            // 403：实例被控制面主动下线，拒绝接入（FR-49），区别于 409 重复 / 404 未注册。
            403 -> RegisterOutcome.OfflineRejected
            401 -> RegisterOutcome.Unauthorized
            400 -> RegisterOutcome.IdentityRequired
            else -> RegisterOutcome.Failed("非预期状态码 ${resp.statusCode}")
        }
    }

    /** v2 身份注册：先走确认状态机，active 后再衔接 legacy 数据面注册。 */
    private fun registerV2(
        identity: AgentIdentity,
        backends: List<String>,
    ): RegisterOutcome {
        val body =
            buildMap {
                put("identityId", identity.identityId)
                put("serverId", identity.serverId)
                put("kind", v2Kind(identity.role))
                put("bootId", identity.bootId)
                if (identity.agentVersion.isNotBlank()) put("agentVersion", identity.agentVersion)
                if (identity.address.isNotBlank()) put("addr", identity.address)
            }
        val resp =
            exec(
                HttpRequest(
                    method = "POST",
                    url = "$base/beacon/v2/agent/register",
                    headers = headers(withBody = true),
                    body = codec.encode(body),
                    readTimeoutMs = settings.requestTimeoutMs,
                ),
            ) ?: return RegisterOutcome.Failed(connectFailReason())

        return when (resp.statusCode) {
            200 -> activeRegisterOutcome(resp.body, identity, backends)
            202 -> pendingApprovalOutcome(resp.body, identity)
            401 -> RegisterOutcome.Unauthorized
            403 -> RegisterOutcome.Rejected
            409 -> RegisterOutcome.IdentityConflict
            400 -> RegisterOutcome.IdentityRequired
            else -> RegisterOutcome.Failed("非预期状态码 ${resp.statusCode}")
        }
    }

    private fun activeRegisterOutcome(
        body: String,
        identity: AgentIdentity,
        backends: List<String>,
    ): RegisterOutcome =
        when (parseRegistrationStatus(body)) {
            "active" -> registerLegacy(identity, backends)
            "disabled" -> RegisterOutcome.Disabled
            "conflict" -> RegisterOutcome.IdentityConflict
            else -> RegisterOutcome.Failed("非预期注册状态")
        }

    private fun pendingApprovalOutcome(
        body: String,
        identity: AgentIdentity,
    ): RegisterOutcome.PendingApproval {
        val obj = JsonTree.asObject(codec.decode(body))
        return RegisterOutcome.PendingApproval(
            serverId = JsonTree.strOr(obj, "serverId", identity.serverId),
            namespace = JsonTree.strOr(obj, "namespace", identity.namespace),
        )
    }

    /** v2 注册确认状态长轮询。 */
    fun pollRegistration(
        identity: AgentIdentity,
        waitSeconds: Int,
    ): RegistrationPollResult {
        if (!identity.hasV2Identity()) return RegistrationPollResult.Failed("缺少 v2 身份")
        val resp =
            exec(
                HttpRequest(
                    method = "GET",
                    url = "$base/beacon/v2/agent/registration?wait=$waitSeconds",
                    headers = v2IdentityHeaders(identity),
                    body = null,
                    readTimeoutMs = waitSeconds * 1000L + settings.requestTimeoutMs,
                ),
            ) ?: return RegistrationPollResult.Failed(connectFailReason())
        return when (resp.statusCode) {
            200 ->
                when (parseRegistrationStatus(resp.body)) {
                    "active" -> RegistrationPollResult.Active
                    "pending" -> RegistrationPollResult.Pending
                    "disabled" -> RegistrationPollResult.Disabled
                    "rejected" -> RegistrationPollResult.Rejected
                    "conflict" -> RegistrationPollResult.Conflict
                    else -> RegistrationPollResult.Failed("非预期注册状态")
                }

            304 -> RegistrationPollResult.NotModified
            else -> RegistrationPollResult.Failed("非预期状态码 ${resp.statusCode}")
        }
    }

    /** 心跳：POST /beacon/v1/agent/heartbeat。404 → 需重新注册。 */
    fun heartbeat(identity: AgentIdentity): HeartbeatOutcome {
        val body =
            mapOf(
                "namespace" to identity.namespace,
                "serverId" to identity.serverId,
            )
        val resp =
            exec(
                HttpRequest(
                    method = "POST",
                    url = "$base/beacon/v1/agent/heartbeat",
                    headers = headers(withBody = true, identity = identity),
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
    fun pollEffective(
        identity: AgentIdentity,
        currentMd5: String?,
        timeoutMs: Long,
    ): PollResult {
        val md5Param = currentMd5 ?: ""
        val url =
            buildString {
                append(base)
                append("/beacon/v1/agent/config/effective")
                append("?namespace=").append(urlEncode(identity.namespace))
                append("&serverId=").append(urlEncode(identity.serverId))
                append("&md5=").append(urlEncode(md5Param))
                append("&timeoutMs=").append(timeoutMs)
            }
        // 读超时给长轮询留余量（挂起上限 + 普通读超时）。
        val resp =
            exec(
                HttpRequest(
                    method = "GET",
                    url = url,
                    headers = headers(withBody = false, identity = identity),
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
        val body =
            buildMap {
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
        val resp =
            exec(
                HttpRequest(
                    method = "POST",
                    url = "$base/beacon/v1/agent/report",
                    headers = headers(withBody = true, identity = identity),
                    body = codec.encode(body),
                    readTimeoutMs = settings.requestTimeoutMs,
                ),
            ) ?: return false
        return resp.statusCode == 200
    }

    /**
     * v2 指标批量上报：POST /beacon/v2/agent/metrics/report（FR-144 §5.1）。同步调用，请在异步线程使用。
     *
     * 报文 `{namespace, serverId, kind, agentTimeMs, droppedSinceLast, samples[]}`；samples 为 5s 批聚合行
     * （§3.1 字段集，snake_case 键，单批 ≤120）。控制面按 `(serverId, bucket_start_ms)` 唯一键去重。
     * 202 受理（回报 accepted / deduplicated + 本批往返 RTT）；429 忙、403 未确认、400 拒绝、其它失败——
     * 均保留缓冲由上报循环重试（仅 202 才 ack 移除已上报样本）。
     */
    fun reportMetricsBatch(
        identity: AgentIdentity,
        kind: MetricKind,
        agentTimeMs: Long,
        droppedSinceLast: Long,
        batches: List<MetricBatch>,
    ): MetricsReportOutcome {
        val body =
            buildMap<String, Any?> {
                put("namespace", identity.namespace)
                put("serverId", identity.serverId)
                put("kind", kind.wire)
                put("agentTimeMs", agentTimeMs)
                put("droppedSinceLast", droppedSinceLast)
                put("samples", batches.map { metricBatchBody(it) })
            }
        val startNanos = System.nanoTime()
        val resp =
            exec(
                HttpRequest(
                    method = "POST",
                    url = "$base/beacon/v2/agent/metrics/report",
                    headers = headers(withBody = true, identity = identity),
                    body = codec.encode(body),
                    readTimeoutMs = settings.requestTimeoutMs,
                ),
            ) ?: return MetricsReportOutcome.Failed(connectFailReason())
        val rttMs = ((System.nanoTime() - startNanos) / 1_000_000L).toInt()
        return when (resp.statusCode) {
            202 -> parseMetricsAccepted(resp.body, rttMs)
            429 -> MetricsReportOutcome.Busy
            403 -> MetricsReportOutcome.Forbidden
            400 -> MetricsReportOutcome.Rejected(parseErrorCode(resp.body))
            else -> MetricsReportOutcome.Failed("非预期状态码 ${resp.statusCode}")
        }
    }

    /**
     * 上报文件资产清单：POST /beacon/v2/agent/assets/manifest（FR-163 §5.1，见 ADR asset-manifest-sync-protocol）。
     * 同步调用，请在异步线程使用。
     *
     * 增量（[mode]=delta）携 [baseDigest] + [upserts] + [deleted]；全量（[mode]=full）携 [uploadId] + [seq] + [eof] + [upserts] 分片。
     * 200 受理（返回应用后 digest + fileCount）；409 基线失配 / 暂存丢失（asset_manifest_out_of_sync，改发全量）；
     * 400 参数错误；连接失败 / 其它 → Failed（fail-static 等下周期）。
     */
    fun reportAssetManifest(
        identity: AgentIdentity,
        meta: AssetManifestMeta,
        baseDigest: String? = null,
        deleted: List<String> = emptyList(),
        uploadId: String? = null,
        seq: Int = 0,
        eof: Boolean = false,
    ): AssetManifestOutcome {
        val body =
            buildMap<String, Any?> {
                put("mode", meta.mode)
                put("scannedAt", isoUtc(meta.scannedAtMs))
                put("scanDurationMs", meta.scanDurationMs)
                put("truncated", meta.truncated)
                put("upserts", meta.upserts.map { assetEntryBody(it) })
                if (meta.mode == "delta") {
                    put("baseDigest", baseDigest ?: "")
                    put("deleted", deleted)
                } else {
                    put("uploadId", uploadId ?: "")
                    put("seq", seq)
                    put("eof", eof)
                }
            }
        val resp =
            exec(
                HttpRequest(
                    method = "POST",
                    url = "$base/beacon/v2/agent/assets/manifest",
                    headers = headers(withBody = true, identity = identity),
                    body = codec.encode(body),
                    readTimeoutMs = settings.requestTimeoutMs,
                ),
            ) ?: return AssetManifestOutcome.Failed(connectFailReason())
        return when (resp.statusCode) {
            200 -> parseAssetManifestAccepted(resp.body)
            409 -> AssetManifestOutcome.OutOfSync
            400 -> AssetManifestOutcome.Rejected(parseErrorCode(resp.body))
            else -> AssetManifestOutcome.Failed("非预期状态码 ${resp.statusCode}")
        }
    }

    /**
     * 拉取调度候选快照：GET /beacon/v2/agent/schedule/candidates（FR-148 §5.1）。同步调用，请在异步线程使用。
     *
     * 无参（服务端按请求方 namespace 圈定）；200 返回 `{generatedAtMs, zones:[{zone, candidates:[...]}]}`
     * （仅 schedulable / degraded 候选）。连接失败 / 非 200 → Failed（本轮放弃刷新，沿用上一快照）。
     */
    fun scheduleCandidates(identity: AgentIdentity): SchedCandidatesOutcome {
        val resp =
            exec(
                HttpRequest(
                    method = "GET",
                    url = "$base/beacon/v2/agent/schedule/candidates",
                    headers = headers(withBody = false, identity = identity),
                    body = null,
                    readTimeoutMs = settings.requestTimeoutMs,
                ),
            ) ?: return SchedCandidatesOutcome.Failed(connectFailReason())
        return when (resp.statusCode) {
            200 -> SchedCandidatesOutcome.Success(parseCandidates(resp.body))
            else -> SchedCandidatesOutcome.Failed("非预期状态码 ${resp.statusCode}")
        }
    }

    /**
     * 请求控制面做一次调度决策：POST /beacon/v2/agent/schedule/decide（FR-148 §5.1）。同步调用，请在异步线程使用。
     *
     * 请求体 `{zone, purpose?, plugin?}`（全 camelCase，purpose/plugin 非空才拼入）；读超时收紧到
     * [SCHED_DECIDE_TIMEOUT_MS]（玩家链路不容久等，超时即由上层降级本地决策）。200 决策成功 / 无候选；
     * 404 zone_not_found；403 cross_namespace；400 参数非法；其它 / 连接失败 → Failed（触发降级）。
     */
    fun scheduleDecide(
        identity: AgentIdentity,
        zone: String,
        purpose: String?,
        plugin: String?,
    ): SchedDecideOutcome {
        val body =
            buildMap<String, Any?> {
                put("zone", zone)
                if (!purpose.isNullOrBlank()) put("purpose", purpose)
                if (!plugin.isNullOrBlank()) put("plugin", plugin)
            }
        val resp =
            exec(
                HttpRequest(
                    method = "POST",
                    url = "$base/beacon/v2/agent/schedule/decide",
                    headers = headers(withBody = true, identity = identity),
                    body = codec.encode(body),
                    readTimeoutMs = SCHED_DECIDE_TIMEOUT_MS,
                ),
            ) ?: return SchedDecideOutcome.Failed(connectFailReason())
        return when (resp.statusCode) {
            200 -> parseDecide(resp.body)
            404 -> SchedDecideOutcome.ZoneNotFound
            403 -> SchedDecideOutcome.CrossNamespace
            400 -> SchedDecideOutcome.Rejected(parseErrorCode(resp.body))
            else -> SchedDecideOutcome.Failed("非预期状态码 ${resp.statusCode}")
        }
    }

    /**
     * 补报降级期本地决策：POST /beacon/v2/agent/schedule/report-local（FR-148 §5.1）。同步调用，请在异步线程使用。
     *
     * 请求体 `{decisions:[{localTraceId, tsMs, zone, plugin, purpose, candidateCount, excluded[], chosenServerId, failReason}]}`
     * （≤100 条/批，全 camelCase，可空字段以空串占位）；控制面按 localTraceId 幂等去重。
     * 202 受理；400 超限 / 非法；403 未确认；其它 / 连接失败 → Failed（保留队列下次恢复再补报）。
     */
    fun reportLocalDecisions(
        identity: AgentIdentity,
        decisions: List<LocalDecisionReport>,
    ): SchedReportLocalOutcome {
        val body = mapOf("decisions" to decisions.map { localDecisionBody(it) })
        val resp =
            exec(
                HttpRequest(
                    method = "POST",
                    url = "$base/beacon/v2/agent/schedule/report-local",
                    headers = headers(withBody = true, identity = identity),
                    body = codec.encode(body),
                    readTimeoutMs = settings.requestTimeoutMs,
                ),
            ) ?: return SchedReportLocalOutcome.Failed(connectFailReason())
        return when (resp.statusCode) {
            202 -> parseReportLocal(resp.body)
            400 -> SchedReportLocalOutcome.Rejected(parseErrorCode(resp.body))
            403 -> SchedReportLocalOutcome.Forbidden
            else -> SchedReportLocalOutcome.Failed("非预期状态码 ${resp.statusCode}")
        }
    }

    /**
     * 连接明细批上报：POST /beacon/v2/agent/connections/batch（FR-145 §5.1，proxy 专用）。同步调用，请在异步线程使用。
     *
     * 报文 `{bootId, droppedCount, events[]}`；events 为 open/close 混合（单批 ≤500，全 camelCase 键），
     * 时间字段上线为 UTC ISO8601。202 受理（accepted / duplicated 计数）；429 队列忙、403 未确认、其它失败——
     * 均保留缓冲由上报循环重试（仅 202 才 ack 移除已上报事件）。
     */
    fun reportConnectionsBatch(
        identity: AgentIdentity,
        bootId: String,
        droppedCount: Long,
        events: List<ConnectionEvent>,
    ): ConnectionsReportOutcome {
        val body =
            buildMap<String, Any?> {
                put("bootId", bootId)
                put("droppedCount", droppedCount)
                put("events", events.map { connectionEventBody(it) })
            }
        val resp =
            exec(
                HttpRequest(
                    method = "POST",
                    url = "$base/beacon/v2/agent/connections/batch",
                    headers = headers(withBody = true, identity = identity),
                    body = codec.encode(body),
                    readTimeoutMs = settings.requestTimeoutMs,
                ),
            ) ?: return ConnectionsReportOutcome.Failed(connectFailReason())
        return when (resp.statusCode) {
            202 -> parseConnectionsAccepted(resp.body)
            429 -> ConnectionsReportOutcome.Busy
            403 -> ConnectionsReportOutcome.Forbidden
            else -> ConnectionsReportOutcome.Failed("非预期状态码 ${resp.statusCode}")
        }
    }

    /**
     * 跨服消息上行发送：POST /beacon/v2/agent/messages/send（FR-149 §5.1，ADR-0063）。同步调用，请在异步线程使用。
     *
     * 报文 `{messageId, msgType, targetKind, targetServerId?|targetPlayerUuid?|targetZone?, correlationId?, payload, sentAt}`
     * （全 camelCase，sentAt 为 UTC ISO8601；targetZone 仅广播 zone 级定向时携带，FR-180）。
     * 200 受理（回报 status）；403 跨域无信任、400 payload 超限等被拒、其它失败。
     */
    fun sendMessage(
        identity: AgentIdentity,
        message: OutboundMessage,
    ): MessageSendOutcome {
        val body =
            buildMap<String, Any?> {
                put("messageId", message.messageId)
                put("msgType", message.msgType)
                put("targetKind", message.targetKind)
                if (message.targetServerId != null) put("targetServerId", message.targetServerId)
                if (message.targetPlayerUuid != null) put("targetPlayerUuid", message.targetPlayerUuid)
                if (message.targetZone != null) put("targetZone", message.targetZone)
                if (message.correlationId != null) put("correlationId", message.correlationId)
                put("payload", message.payload)
                put("sentAt", isoUtc(message.sentAtMs))
            }
        val resp =
            exec(
                HttpRequest(
                    method = "POST",
                    url = "$base/beacon/v2/agent/messages/send",
                    headers = headers(withBody = true, identity = identity),
                    body = codec.encode(body),
                    readTimeoutMs = settings.requestTimeoutMs,
                ),
            ) ?: return MessageSendOutcome.Failed(connectFailReason())
        return when (resp.statusCode) {
            200 -> parseMessageSent(resp.body, message.messageId)
            403 -> MessageSendOutcome.Forbidden
            400 -> MessageSendOutcome.Rejected(parseErrorCode(resp.body))
            else -> MessageSendOutcome.Failed("非预期状态码 ${resp.statusCode}")
        }
    }

    /**
     * 跨服消息下行长轮询：POST /beacon/v2/agent/messages/poll（FR-149 §5.1）。同步调用，请在异步线程使用。
     *
     * 报文 `{waitSec, max}`（waitSec ≤25、max ≤50）。200 返回 `{messages[]}`（本服待投消息）；204 无消息超时。
     * 读超时给长轮询留余量（挂起上限 + 普通读超时）。
     */
    fun pollMessages(
        identity: AgentIdentity,
        waitSec: Int,
        max: Int,
    ): MessagePollOutcome {
        val body = mapOf("waitSec" to waitSec, "max" to max)
        val resp =
            exec(
                HttpRequest(
                    method = "POST",
                    url = "$base/beacon/v2/agent/messages/poll",
                    headers = headers(withBody = true, identity = identity),
                    body = codec.encode(body),
                    readTimeoutMs = waitSec * 1000L + settings.requestTimeoutMs,
                ),
            ) ?: return MessagePollOutcome.Failed(connectFailReason())
        return when (resp.statusCode) {
            200 -> MessagePollOutcome.Messages(parsePolledMessages(resp.body))
            204 -> MessagePollOutcome.Empty
            else -> MessagePollOutcome.Failed("非预期状态码 ${resp.statusCode}")
        }
    }

    /**
     * 跨服消息回执：POST /beacon/v2/agent/messages/ack（FR-149 §5.1）。同步调用，请在异步线程使用。
     *
     * 报文 `{results:[{messageId, status, reason?, deliveredAt, handlerCostMs?}]}`（status delivered/failed，
     * deliveredAt 为 UTC ISO8601）。200 返回 `{applied, ignored}`（未知 messageId 计入 ignored）。
     */
    fun ackMessages(
        identity: AgentIdentity,
        acks: List<MessageAck>,
    ): MessageAckOutcome {
        val body = mapOf("results" to acks.map { messageAckBody(it) })
        val resp =
            exec(
                HttpRequest(
                    method = "POST",
                    url = "$base/beacon/v2/agent/messages/ack",
                    headers = headers(withBody = true, identity = identity),
                    body = codec.encode(body),
                    readTimeoutMs = settings.requestTimeoutMs,
                ),
            ) ?: return MessageAckOutcome.Failed(connectFailReason())
        return when (resp.statusCode) {
            200 -> parseMessageAckApplied(resp.body)
            else -> MessageAckOutcome.Failed("非预期状态码 ${resp.statusCode}")
        }
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
    ): List<Map<String, Any?>> = discover(DiscoveryFilters(namespace = namespace, group = group, zone = zone, role = role, tags = tags))

    fun discover(
        filters: DiscoveryFilters,
        identity: AgentIdentity? = null,
    ): List<Map<String, Any?>> {
        val params = StringBuilder()
        appendParam(params, "namespace", filters.namespace)
        appendParam(params, "group", filters.group)
        appendParam(params, "zone", filters.zone)
        appendParam(params, "role", filters.role)
        // 自定义元数据过滤：每个键拼为 tag.<key>=<value>，控制面按 metadata 键值匹配。
        for ((k, v) in filters.tags) {
            appendParam(params, "tag.$k", v)
        }
        val url = "$base/beacon/v1/agent/discovery" + if (params.isEmpty()) "" else "?$params"

        val resp =
            exec(
                HttpRequest(
                    method = "GET",
                    url = url,
                    headers = headers(withBody = false, identity = identity),
                    body = null,
                    readTimeoutMs = settings.requestTimeoutMs,
                ),
            )
        return if (resp?.statusCode == 200) {
            val obj = JsonTree.asObject(codec.decode(resp.body))
            JsonTree.asList(obj["instances"]).map { JsonTree.asObject(it) }
        } else {
            emptyList()
        }
    }

    /**
     * 长轮询文件清单：GET /beacon/v1/agent/files/manifest（通道B）。
     *
     * 带当前 fileTreeMd5；变了 200 返回新清单（path→md5，不含内容），未变到超时 304。
     * 与配置长轮询唤醒集合独立（见 ADR-0010）。
     */
    fun pollFileManifest(
        identity: AgentIdentity,
        currentMd5: String?,
        timeoutMs: Long,
    ): FileManifestPollResult {
        val md5Param = currentMd5 ?: ""
        val url =
            buildString {
                append(base)
                append("/beacon/v1/agent/files/manifest")
                append("?namespace=").append(urlEncode(identity.namespace))
                append("&serverId=").append(urlEncode(identity.serverId))
                append("&md5=").append(urlEncode(md5Param))
                append("&timeoutMs=").append(timeoutMs)
            }
        // 读超时给长轮询留余量（挂起上限 + 普通读超时）。
        val resp =
            exec(
                HttpRequest(
                    method = "GET",
                    url = url,
                    headers = headers(withBody = false, identity = identity),
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
    fun fetchFileContent(
        identity: AgentIdentity,
        path: String,
    ): FileContent? {
        val url =
            buildString {
                append(base)
                append("/beacon/v1/agent/files/content")
                append("?namespace=").append(urlEncode(identity.namespace))
                append("&serverId=").append(urlEncode(identity.serverId))
                append("&path=").append(urlEncode(path))
            }
        val resp =
            exec(
                HttpRequest(
                    method = "GET",
                    url = url,
                    headers = headers(withBody = false, identity = identity),
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
    fun pollOverrideSets(
        identity: AgentIdentity,
        currentMd5: String?,
        timeoutMs: Long,
    ): OverridePollResult {
        val md5Param = currentMd5 ?: ""
        val url =
            buildString {
                append(base)
                append("/beacon/v1/agent/override-sets")
                append("?namespace=").append(urlEncode(identity.namespace))
                append("&serverId=").append(urlEncode(identity.serverId))
                append("&md5=").append(urlEncode(md5Param))
                append("&timeoutMs=").append(timeoutMs)
            }
        val resp =
            exec(
                HttpRequest(
                    method = "GET",
                    url = url,
                    headers = headers(withBody = false, identity = identity),
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
    fun fetchOverrideMember(
        identity: AgentIdentity,
        setName: String,
        path: String,
    ): FileContent? {
        val url =
            buildString {
                append(base)
                append("/beacon/v1/agent/override-sets/content")
                append("?namespace=").append(urlEncode(identity.namespace))
                append("&serverId=").append(urlEncode(identity.serverId))
                append("&set=").append(urlEncode(setName))
                append("&path=").append(urlEncode(path))
            }
        val resp =
            exec(
                HttpRequest(
                    method = "GET",
                    url = url,
                    headers = headers(withBody = false, identity = identity),
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
     * 拉取本 agent 一条待办命令：GET /beacon/v1/agent/commands（FR-39，见 ADR-0027）。同步调用，请在异步线程使用。
     *
     * 收到 SSE command-pending 事件 / SSE READY 后调用。
     * 200 返回命令（控制面已 CAS 迁移 fetched）；204 无待办返回 null；其它状态（404 未注册 / 连接失败）一律返回 null
     * （命令流为 best-effort：拉不到本轮静默放弃，下次事件 / 重连再拉，不影响配置主流程）。
     */
    fun fetchPendingCommand(identity: AgentIdentity): AgentCommand? {
        val url =
            buildString {
                append(base)
                append("/beacon/v1/agent/commands")
                append("?namespace=").append(urlEncode(identity.namespace))
                append("&serverId=").append(urlEncode(identity.serverId))
            }
        val resp =
            exec(
                HttpRequest(
                    method = "GET",
                    url = url,
                    headers = headers(withBody = false, identity = identity),
                    body = null,
                    readTimeoutMs = settings.requestTimeoutMs,
                ),
            ) ?: return null
        return when (resp.statusCode) {
            200 -> parsePendingCommand(resp.body)
            204 -> null // 无待办命令
            else -> null // 404 未注册 / 其它：本轮放弃（best-effort）
        }
    }

    /**
     * 回传反向抓取结果：POST /beacon/v1/agent/files/ingest（FR-39，见 ADR-0027）。同步调用，请在异步线程使用。
     *
     * 携带命令 id + 文件集（path→content 文本）。控制面入库前同口径再校验（双保险）后复用 FileService.Import 落覆盖。
     * 200 视作成功；其它（命令态不符 / 校验失败 / 连接失败）返回 false（命令在控制面侧标 failed，agent 侧不重传）。
     */
    fun uploadIngest(
        commandId: Long,
        files: List<IngestFile>,
        identity: AgentIdentity? = null,
    ): Boolean {
        val body =
            mapOf(
                "commandId" to commandId,
                "files" to files.map { mapOf("path" to it.path, "content" to it.content) },
            )
        val resp =
            exec(
                HttpRequest(
                    method = "POST",
                    url = "$base/beacon/v1/agent/files/ingest",
                    headers = headers(withBody = true, identity = identity),
                    body = codec.encode(body),
                    readTimeoutMs = settings.requestTimeoutMs,
                ),
            ) ?: return false
        return resp.statusCode == 200
    }

    /**
     * 回传反向抓取 scan 清单：POST /beacon/v1/agent/files/scan（FR-58，见 ADR-0037）。同步调用，请在异步线程使用。
     *
     * 携带命令 id + 元信息清单（path/size/isText/overThreshold，**无 content**）。控制面存入任务 manifest、计数、转 pending-review。
     * 200 视作成功；其它（命令态不符 / 连接失败）返回 false（命令在控制面侧标 failed，agent 侧不重传）。
     */
    fun uploadScan(
        commandId: Long,
        files: List<ScanFile>,
        identity: AgentIdentity? = null,
    ): Boolean {
        val body =
            mapOf(
                "commandId" to commandId,
                "files" to
                    files.map {
                        mapOf(
                            "path" to it.path,
                            "size" to it.size,
                            "isText" to it.isText,
                            "overThreshold" to it.overThreshold,
                        )
                    },
            )
        val resp =
            exec(
                HttpRequest(
                    method = "POST",
                    url = "$base/beacon/v1/agent/files/scan",
                    headers = headers(withBody = true, identity = identity),
                    body = codec.encode(body),
                    readTimeoutMs = settings.requestTimeoutMs,
                ),
            ) ?: return false
        return resp.statusCode == 200
    }

    /**
     * 回传反向抓取执行错误：POST /beacon/v1/agent/files/error（FR-87）。同步调用，请在异步线程使用。
     *
     * agent 执行 scan/submit 读盘失败（IO 错 / 异常）时调用，把错误明细带给控制面，使任务转 failed 并记 lastError，
     * 不再让任务静默卡在非终态等过期清理。携命令 id + 原因文本。
     * 200 视作成功；其它（命令态不符 / 连接失败）返回 false（best-effort、不重试——回传不通仍交控制面超时清理为 expired）。
     */
    fun uploadError(
        commandId: Long,
        reason: String,
        identity: AgentIdentity? = null,
    ): Boolean {
        val body =
            mapOf(
                "commandId" to commandId,
                "reason" to reason,
            )
        val resp =
            exec(
                HttpRequest(
                    method = "POST",
                    url = "$base/beacon/v1/agent/files/error",
                    headers = headers(withBody = true, identity = identity),
                    body = codec.encode(body),
                    readTimeoutMs = settings.requestTimeoutMs,
                ),
            ) ?: return false
        return resp.statusCode == 200
    }

    /**
     * 回传 agent 自身日志快照：POST /beacon/v1/agent/logs（FR-88，见 ADR-0040）。同步调用，请在异步线程使用。
     *
     * 携带命令 id + 日志行集（级别 + 已脱敏文本）。日志在 agent 侧落环形缓冲那一刻即脱敏，本方法只忠实回传缓冲快照。
     * 控制面把日志行存为命令瞬态（取完即弃、不入真源、不进审计 detail）后 CAS done。
     * 200 视作成功；其它（命令态不符 / 连接失败）返回 false（命令在控制面侧标 failed / 超时清理，agent 侧不重传）。
     */
    fun uploadLogs(
        commandId: Long,
        lines: List<LogLine>,
        identity: AgentIdentity? = null,
    ): Boolean {
        val body =
            mapOf(
                "commandId" to commandId,
                "lines" to lines.map { mapOf("level" to it.level, "text" to it.text) },
            )
        val resp =
            exec(
                HttpRequest(
                    method = "POST",
                    url = "$base/beacon/v1/agent/logs",
                    headers = headers(withBody = true, identity = identity),
                    body = codec.encode(body),
                    readTimeoutMs = settings.requestTimeoutMs,
                ),
            ) ?: return false
        return resp.statusCode == 200
    }

    /**
     * 回传命令执行结果：POST /beacon/v1/agent/commands/result（FR-91）。同步调用，请在异步线程使用。
     *
     * 用于强制重同步（resync-config）这类无内容回传、仅需推进命令生命周期的命令：携命令 id + ok（成功）+ 失败原因。
     * 控制面据此 CAS 命令 done / failed。200 视作成功；其它（命令态不符 / 连接失败）返回 false（控制面侧超时清理兜底，agent 不重传）。
     */
    fun uploadCommandResult(
        commandId: Long,
        ok: Boolean,
        reason: String,
        identity: AgentIdentity? = null,
    ): Boolean {
        val body =
            mapOf(
                "commandId" to commandId,
                "ok" to ok,
                "reason" to reason,
            )
        val resp =
            exec(
                HttpRequest(
                    method = "POST",
                    url = "$base/beacon/v1/agent/commands/result",
                    headers = headers(withBody = true, identity = identity),
                    body = codec.encode(body),
                    readTimeoutMs = settings.requestTimeoutMs,
                ),
            ) ?: return false
        return resp.statusCode == 200
    }

    /**
     * 回传文件浏览结果：POST /beacon/v1/agent/files/browse-result（FR-110，见 ADR-0049）。同步调用，请在异步线程使用。
     *
     * 携命令 id + 身份（namespace/serverId）+ ok + 结果对象（[result]：列目录 / 子树 / 文件内容的结构化 Map）或失败原因。
     * ok=true 时 [result] 非空、[reason] 空；ok=false（原语返回 null：越权 / 非目录 / 非文本）时 [result] 空、[reason] 携原因。
     * 控制面据此把结果转存命令瞬态 + CAS done / failed，并唤醒等待中的 admin。
     * 200 视作成功；其它（命令态不符 / 连接失败）返回 false（best-effort、不重试——控制面超时清理兜底）。
     */
    fun uploadBrowseResult(
        identity: AgentIdentity,
        commandId: Long,
        ok: Boolean,
        result: Map<String, Any?>?,
        reason: String,
    ): Boolean {
        val body =
            buildMap {
                put("namespace", identity.namespace)
                put("serverId", identity.serverId)
                put("commandId", commandId)
                put("ok", ok)
                if (result != null) put("result", result)
                if (reason.isNotEmpty()) put("reason", reason)
            }
        val resp =
            exec(
                HttpRequest(
                    method = "POST",
                    url = "$base/beacon/v1/agent/files/browse-result",
                    headers = headers(withBody = true, identity = identity),
                    body = codec.encode(body),
                    readTimeoutMs = settings.requestTimeoutMs,
                ),
            ) ?: return false
        return resp.statusCode == 200
    }

    /**
     * 回传文件资产内容：POST /beacon/v2/agent/assets/content（FR-164，见 v2-file-assets.md §5.1）。同步调用，请在异步线程使用。
     *
     * 携命令 id + 身份（namespace/serverId）+ 内容元数据（binary/truncated/content）或读失败原因（error）。
     * [asset] 非空=读成功（含二进制回元数据）；null=读失败（越权 / 不存在 / 读盘异常），回**已脱敏**的通用原因。
     * 控制面据命令 id 定位命令（不依赖回传 path），内容为受审计的瞬态数据、转存内存中继唤醒等待的 admin，**绝不落库**。
     * 200 视作成功；其它（命令态不符 / 连接失败）返回 false（best-effort、不重试——控制面超时清理兜底）。
     */
    fun postAssetContent(
        identity: AgentIdentity,
        commandId: Long,
        asset: AssetContent?,
    ): Boolean {
        val body =
            buildMap<String, Any?> {
                put("commandId", commandId)
                put("namespace", identity.namespace)
                put("serverId", identity.serverId)
                if (asset != null) {
                    put("binary", asset.binary)
                    put("truncated", asset.truncated)
                    put("content", asset.content)
                } else {
                    put("error", "目标不存在或不可读") // 已脱敏、无凭据；真实原因仅落 agent 本地日志
                }
            }
        val resp =
            exec(
                HttpRequest(
                    method = "POST",
                    url = "$base/beacon/v2/agent/assets/content",
                    headers = headers(withBody = true, identity = identity),
                    body = codec.encode(body),
                    readTimeoutMs = settings.requestTimeoutMs,
                ),
            ) ?: return false
        return resp.statusCode == 200
    }

    /**
     * agent 面鉴权头（X-Beacon-Token + v2 X-Beacon-Identity / X-Beacon-Boot），供交付流式数据面
     * （[top.wcpe.beacon.agent.core.transport.BlobStreamTransport]）复用同一鉴权真源（FR-165，见 ADR-0069）。
     *
     * 不含 Content-Type：blob PUT 的内容类型由流式适配器按 octet-stream 设置，本头只承载鉴权。
     */
    fun agentAuthHeaders(identity: AgentIdentity): Map<String, String> = headers(withBody = false, identity = identity)

    /**
     * 拉取模板源待上传 blob 清单：GET /beacon/v2/agent/delivery/orders/{id}/upload-manifest（FR-165，spec §5.2）。
     * 同步调用，请在异步线程使用。
     *
     * 200 返回待上传项（path/sha256/size，已就绪 blob 不在列）；其它（403 非模板源 / 404 单不存在 / 连接失败）返回 null
     * （交付上传流程据此回执 failed）。
     */
    fun fetchDeliveryUploadManifest(
        identity: AgentIdentity,
        orderId: Long,
    ): DeliveryUploadManifest? {
        val resp =
            exec(
                HttpRequest(
                    method = "GET",
                    url = "$base/beacon/v2/agent/delivery/orders/$orderId/upload-manifest",
                    headers = headers(withBody = false, identity = identity),
                    body = null,
                    readTimeoutMs = settings.requestTimeoutMs,
                ),
            ) ?: return null
        if (resp.statusCode != 200) return null
        return parseDeliveryUploadManifest(resp.body)
    }

    /**
     * 拉取目标本服差异清单：GET /beacon/v2/agent/delivery/orders/{id}/manifest（FR-165，spec §5.2）。
     * 同步调用，请在异步线程使用。
     *
     * 200 返回文件差异项（path/action/sha256/size）与生效方式（配置项摘要 M4 消费，本期不解析）；
     * 其它（403 非目标 / 404 / 连接失败）返回 null（推送流程据此回执 failed）。
     */
    fun fetchDeliveryManifest(
        identity: AgentIdentity,
        orderId: Long,
    ): DeliveryTargetManifest? {
        val resp =
            exec(
                HttpRequest(
                    method = "GET",
                    url = "$base/beacon/v2/agent/delivery/orders/$orderId/manifest",
                    headers = headers(withBody = false, identity = identity),
                    body = null,
                    readTimeoutMs = settings.requestTimeoutMs,
                ),
            ) ?: return null
        if (resp.statusCode != 200) return null
        return parseDeliveryManifest(resp.body)
    }

    /**
     * 回执交付阶段结果：POST /beacon/v2/agent/delivery/orders/{id}/result（FR-165，spec §5.2）。
     * 同步调用，请在异步线程使用。
     *
     * 报文字段（phase / status / 计数 / backupPresent / 脱敏 error）打包在 [report] 中。
     * 204 视作成功；其它（命令态不符 / 连接失败）返回 false（best-effort，控制面按命令超时清理兜底）。
     */
    fun postDeliveryResult(
        identity: AgentIdentity,
        orderId: Long,
        report: DeliveryStageReport,
    ): Boolean {
        val body =
            buildMap<String, Any?> {
                put("phase", report.phase)
                put("status", report.status)
                put("changedFileCount", report.changedFileCount)
                put("skippedFileCount", report.skippedFileCount)
                put("backupPresent", report.backupPresent)
                put("error", report.error)
            }
        val resp =
            exec(
                HttpRequest(
                    method = "POST",
                    url = "$base/beacon/v2/agent/delivery/orders/$orderId/result",
                    headers = headers(withBody = true, identity = identity),
                    body = codec.encode(body),
                    readTimeoutMs = settings.requestTimeoutMs,
                ),
            ) ?: return false
        return resp.statusCode == 204
    }

    /** 解析待上传清单响应（orderId + items[path/sha256/size]，camelCase 键，缺失项按空 / 0 兜底）。 */
    private fun parseDeliveryUploadManifest(jsonBody: String): DeliveryUploadManifest {
        val obj = JsonTree.asObject(codec.decode(jsonBody))
        val items =
            JsonTree.asList(obj["items"]).map { raw ->
                val itemObj = JsonTree.asObject(raw)
                DeliveryUploadItem(
                    path = JsonTree.strOr(itemObj, "path", ""),
                    sha256 = JsonTree.strOr(itemObj, "sha256", ""),
                    sizeBytes = JsonTree.longOr(itemObj, "size", 0L),
                )
            }
        return DeliveryUploadManifest(orderId = JsonTree.longOr(obj, "orderId", 0L), items = items)
    }

    /** 解析目标差异清单响应（orderId + activationMethod + files[path/action/sha256/size]；configs 归 M4，不解析）。 */
    private fun parseDeliveryManifest(jsonBody: String): DeliveryTargetManifest {
        val obj = JsonTree.asObject(codec.decode(jsonBody))
        val files =
            JsonTree.asList(obj["files"]).map { raw ->
                val fileObj = JsonTree.asObject(raw)
                DeliveryManifestFile(
                    path = JsonTree.strOr(fileObj, "path", ""),
                    action = JsonTree.strOr(fileObj, "action", ""),
                    sha256 = JsonTree.strOr(fileObj, "sha256", ""),
                    sizeBytes = JsonTree.longOr(fileObj, "size", 0L),
                )
            }
        return DeliveryTargetManifest(
            orderId = JsonTree.longOr(obj, "orderId", 0L),
            activationMethod = JsonTree.strOr(obj, "activationMethod", ""),
            files = files,
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

    private fun parseRegistrationStatus(jsonBody: String): String {
        val obj = JsonTree.asObject(codec.decode(jsonBody))
        return JsonTree.strOr(obj, "status", "")
    }

    private fun parseEffective(jsonBody: String): EffectiveResult {
        val obj = JsonTree.asObject(codec.decode(jsonBody))
        val items =
            JsonTree.asList(obj["items"]).map { raw ->
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
        val entries =
            JsonTree.asList(obj["files"]).map { raw ->
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
        val sets =
            JsonTree.asList(obj["sets"]).map { raw ->
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

    /**
     * 解析待办命令响应（FR-39 / FR-58）：
     * `{"id":<n>,"type":"ingest-plugins","payload":{"scope","group","target","mode","selectedPaths":[...]}}`。
     *
     * payload 缺失字段按空兜底；`mode` 区分 scan / submit（缺失=旧整树行为，向后兼容），`selectedPaths` 仅 submit 用。
     * agent 据 mode/selectedPaths 决定两段式分路（见 ReverseFetchExecutor）；scope/group/target 仍仅供日志可读 + 回传 id。
     */
    private fun parsePendingCommand(jsonBody: String): AgentCommand {
        val obj = JsonTree.asObject(codec.decode(jsonBody))
        val payloadObj = JsonTree.asObject(obj["payload"])
        val type = JsonTree.strOr(obj, "type", "")
        // 交付命令（FR-165）：payload 只含 orderId，解析到独立的 DeliveryCommandPayload（不膨胀 IngestCommandPayload）。
        val deliveryPayload =
            if (AgentCommand.isDeliveryType(type)) DeliveryCommandPayload(orderId = JsonTree.longOr(payloadObj, "orderId", 0L)) else null
        return AgentCommand(
            id = JsonTree.longOr(obj, "id", 0L),
            type = type,
            deliveryPayload = deliveryPayload,
            payload =
                IngestCommandPayload(
                    scope = JsonTree.strOr(payloadObj, "scope", ""),
                    group = JsonTree.strOr(payloadObj, "group", ""),
                    target = JsonTree.strOr(payloadObj, "target", ""),
                    mode = JsonTree.strOr(payloadObj, "mode", ""),
                    selectedPaths = JsonTree.asList(payloadObj["selectedPaths"]).map { JsonTree.asString(it) },
                    // 文件浏览字段（FR-110，仅 fs-browse 命令携带；其它命令缺省为空，向后兼容）。
                    op = JsonTree.strOr(payloadObj, "op", ""),
                    path = JsonTree.strOr(payloadObj, "path", ""),
                    offset = JsonTree.intOr(payloadObj, "offset", 0),
                    limit = JsonTree.intOr(payloadObj, "limit", 0),
                    maxDepth = JsonTree.intOr(payloadObj, "maxDepth", 0),
                    // 文件资产重扫字段（FR-163，仅 asset-rescan 命令携带；其它命令缺省 false，向后兼容）。
                    force = JsonTree.boolOr(payloadObj, "force", false),
                    // 文件资产读取上限（FR-164，仅 asset-read 命令携带；其它命令缺省 0，向后兼容）。
                    maxBytes = JsonTree.intOr(payloadObj, "maxBytes", 0),
                ),
        )
    }

    /**
     * 把一个 5s 批聚合行拼成 v2 指标上报 samples 元素（FR-144）。
     *
     * 线上键与信封同为 **camelCase**（v2 API 通用约定；控制面接收结构体 json tag 亦 camelCase，再映射到 §3.1 的
     * snake_case DB 列——§3.1 是库表列名、非线上键）。每行含全部指标维度，不适用维度写缺省值（数值 0、后端 RTT -1），
     * 由控制面按 kind 解释，不特判 NULL。
     */
    private fun metricBatchBody(batch: MetricBatch): Map<String, Any?> {
        val backend = batch.payload as? BackendBatch
        val proxy = batch.payload as? ProxyBatch
        return mapOf(
            "bucketStartMs" to batch.bucketStartMs,
            "sampleCount" to batch.sampleCount,
            "cpuPctAvg" to batch.load.cpuPctAvg,
            "cpuPctMax" to batch.load.cpuPctMax,
            "memUsedMbAvg" to batch.load.memUsedMbAvg,
            "memMaxMb" to batch.load.memMaxMb,
            "tpsAvg" to (backend?.tpsAvg ?: 0.0),
            "tpsMin" to (backend?.tpsMin ?: 0.0),
            "onlineAvg" to (backend?.onlineAvg ?: 0),
            "onlineMax" to (backend?.onlineMax ?: 0),
            "maxOnline" to (backend?.maxOnline ?: 0),
            "connAvg" to (proxy?.connAvg ?: 0),
            "connMax" to (proxy?.connMax ?: 0),
            "backendUp" to (proxy?.backendUp ?: 0),
            "backendTotal" to (proxy?.backendTotal ?: 0),
            "backendRttMsAvg" to (proxy?.backendRttMsAvg ?: MetricSample.RTT_UNAVAILABLE),
            "reportRttMs" to batch.reportRttMs,
        )
    }

    /** 解析 202 受理响应（accepted / deduplicated 计数 + 顺带回传的自身健康 self），并带上本批上报 RTT。 */
    private fun parseMetricsAccepted(
        jsonBody: String,
        rttMs: Int,
    ): MetricsReportOutcome.Accepted {
        val obj = JsonTree.asObject(codec.decode(jsonBody))
        return MetricsReportOutcome.Accepted(
            accepted = JsonTree.intOr(obj, "accepted", 0),
            deduplicated = JsonTree.intOr(obj, "deduplicated", 0),
            rttMs = rttMs,
            self = parseSelfHealth(obj["self"]),
        )
    }

    /** 把一个资产清单条目拼成上报报文 upserts 元素（键集 camelCase，与控制面接收结构体对齐，FR-163）。 */
    private fun assetEntryBody(entry: AssetEntry): Map<String, Any?> =
        mapOf(
            "path" to entry.path,
            "sha256" to entry.sha256,
            "size" to entry.size,
            "mtimeMs" to entry.mtimeMs,
            "isText" to entry.isText,
        )

    /** 解析文件资产清单 200 受理响应（应用后清单摘要 + 文件数）。 */
    private fun parseAssetManifestAccepted(jsonBody: String): AssetManifestOutcome.Accepted {
        val obj = JsonTree.asObject(codec.decode(jsonBody))
        return AssetManifestOutcome.Accepted(
            digest = JsonTree.strOr(obj, "digest", ""),
            fileCount = JsonTree.intOr(obj, "fileCount", 0),
        )
    }

    /** 解析 self 健康段（FR-147/FR-148）；缺失 / 非对象（含 JSON null）返回 null。 */
    private fun parseSelfHealth(raw: Any?): SelfHealth? {
        if (raw !is Map<*, *>) return null
        val obj = JsonTree.asObject(raw)
        return SelfHealth(
            score = JsonTree.intOr(obj, "score", 0),
            level = JsonTree.strOr(obj, "level", ""),
            schedulable = JsonTree.boolOr(obj, "schedulable", false),
            reasons = JsonTree.asList(obj["reasons"]).map { JsonTree.asString(it) },
        )
    }

    /** 解析 candidates 200 响应（generatedAtMs + 各 zone 候选）。 */
    private fun parseCandidates(jsonBody: String): SchedCandidates {
        val obj = JsonTree.asObject(codec.decode(jsonBody))
        val zones =
            JsonTree.asList(obj["zones"]).map { rawZone ->
                val zoneObj = JsonTree.asObject(rawZone)
                ZoneCandidates(
                    zone = JsonTree.strOr(zoneObj, "zone", ""),
                    candidates =
                        JsonTree.asList(zoneObj["candidates"]).map { rawCand ->
                            val c = JsonTree.asObject(rawCand)
                            CandidateEntry(
                                serverId = JsonTree.strOr(c, "serverId", ""),
                                score = JsonTree.intOr(c, "score", 0),
                                level = JsonTree.strOr(c, "level", ""),
                                schedulable = JsonTree.boolOr(c, "schedulable", true),
                                onlineCount = JsonTree.intOr(c, "onlineCount", 0),
                                maxOnline = JsonTree.intOr(c, "maxOnline", 0),
                            )
                        },
                )
            }
        return SchedCandidates(generatedAtMs = JsonTree.longOr(obj, "generatedAtMs", 0L), zones = zones)
    }

    /** 解析 decide 200 响应（chosen 可空、failReason 可空）。 */
    private fun parseDecide(jsonBody: String): SchedDecideOutcome.Decided {
        val obj = JsonTree.asObject(codec.decode(jsonBody))
        val chosenRaw = obj["chosen"]
        val chosen =
            if (chosenRaw is Map<*, *>) {
                val c = JsonTree.asObject(chosenRaw)
                DecidedChoice(JsonTree.strOr(c, "serverId", ""), JsonTree.intOr(c, "score", 0))
            } else {
                null
            }
        return SchedDecideOutcome.Decided(
            traceId = JsonTree.strOr(obj, "traceId", ""),
            chosen = chosen,
            candidateCount = JsonTree.intOr(obj, "candidateCount", 0),
            excludedCount = JsonTree.intOr(obj, "excludedCount", 0),
            failReason = JsonTree.str(obj, "failReason"),
        )
    }

    /** 解析 report-local 202 响应（accepted / deduplicated 计数）。 */
    private fun parseReportLocal(jsonBody: String): SchedReportLocalOutcome.Accepted {
        val obj = JsonTree.asObject(codec.decode(jsonBody))
        return SchedReportLocalOutcome.Accepted(
            accepted = JsonTree.intOr(obj, "accepted", 0),
            deduplicated = JsonTree.intOr(obj, "deduplicated", 0),
        )
    }

    /** 把一条本地决策补报记录拼成 report-local 的 decisions 元素（全 camelCase，可空字段空串占位）。 */
    private fun localDecisionBody(report: LocalDecisionReport): Map<String, Any?> =
        mapOf(
            "localTraceId" to report.localTraceId,
            "tsMs" to report.tsMs,
            "zone" to report.zone,
            "plugin" to (report.plugin ?: ""),
            "purpose" to (report.purpose ?: ""),
            "candidateCount" to report.candidateCount,
            "excluded" to report.excluded.map { mapOf("serverId" to it.serverId, "reason" to it.reason) },
            "chosenServerId" to (report.chosenServerId ?: ""),
            "failReason" to (report.failReason ?: ""),
        )

    /** 把一条连接事件拼成 batch 报文的 events 元素（全 camelCase，空可选字段省略；时间 UTC ISO8601）。 */
    private fun connectionEventBody(event: ConnectionEvent): Map<String, Any?> =
        buildMap {
            put("kind", event.kind.wire)
            put("connId", event.connId)
            put("playerUuid", event.playerUuid)
            put("playerName", event.playerName)
            if (event.clientIp != null) put("clientIp", event.clientIp)
            if (event.protocolVersion != null) put("protocolVersion", event.protocolVersion)
            put("openedAt", isoUtc(event.openedAtMs))
            if (event.closedAtMs != null) put("closedAt", isoUtc(event.closedAtMs))
            if (event.closeKind != null) put("closeKind", event.closeKind)
            if (event.closeReason != null) put("closeReason", event.closeReason)
            if (event.firstBackend != null) put("firstBackend", event.firstBackend)
            if (event.lastBackend != null) put("lastBackend", event.lastBackend)
            if (event.backendSwitchCount != null) put("backendSwitchCount", event.backendSwitchCount)
        }

    /** 解析连接批上报 202 响应（accepted / duplicated 计数）。 */
    private fun parseConnectionsAccepted(jsonBody: String): ConnectionsReportOutcome.Accepted {
        val obj = JsonTree.asObject(codec.decode(jsonBody))
        return ConnectionsReportOutcome.Accepted(
            accepted = JsonTree.intOr(obj, "accepted", 0),
            duplicated = JsonTree.intOr(obj, "duplicated", 0),
        )
    }

    /** 解析消息发送 200 响应（messageId + status；messageId 缺失回退本地生成值）。 */
    private fun parseMessageSent(
        jsonBody: String,
        fallbackId: String,
    ): MessageSendOutcome.Ok {
        val obj = JsonTree.asObject(codec.decode(jsonBody))
        return MessageSendOutcome.Ok(
            messageId = JsonTree.strOr(obj, "messageId", fallbackId),
            status = JsonTree.strOr(obj, "status", ""),
        )
    }

    /** 解析长轮询 200 响应（messages 数组 → PolledMessage）。 */
    private fun parsePolledMessages(jsonBody: String): List<PolledMessage> {
        val obj = JsonTree.asObject(codec.decode(jsonBody))
        return JsonTree.asList(obj["messages"]).map { raw ->
            val m = JsonTree.asObject(raw)
            PolledMessage(
                messageId = JsonTree.strOr(m, "messageId", ""),
                msgType = JsonTree.strOr(m, "msgType", ""),
                sourceServerId = JsonTree.strOr(m, "sourceServerId", ""),
                correlationId = JsonTree.str(m, "correlationId"),
                payload = m["payload"],
                createdAt = JsonTree.strOr(m, "createdAt", ""),
                // 广播投递标记（additive 键，FR-180）：定向消息不带，缺省 false。
                broadcast = JsonTree.boolOr(m, "broadcast", false),
            )
        }
    }

    /** 把一条回执拼成 ack 报文的 results 元素（全 camelCase，空可选字段省略；deliveredAt 为 UTC ISO8601）。 */
    private fun messageAckBody(ack: MessageAck): Map<String, Any?> =
        buildMap {
            put("messageId", ack.messageId)
            put("status", ack.status)
            if (ack.reason != null) put("reason", ack.reason)
            put("deliveredAt", isoUtc(ack.deliveredAtMs))
            if (ack.handlerCostMs != null) put("handlerCostMs", ack.handlerCostMs)
        }

    /** 解析回执 200 响应（applied / ignored 计数）。 */
    private fun parseMessageAckApplied(jsonBody: String): MessageAckOutcome.Applied {
        val obj = JsonTree.asObject(codec.decode(jsonBody))
        return MessageAckOutcome.Applied(
            applied = JsonTree.intOr(obj, "applied", 0),
            ignored = JsonTree.intOr(obj, "ignored", 0),
        )
    }

    /** Unix 毫秒 → UTC ISO8601（如 2026-07-06T08:00:00.123Z），与控制面时间口径一致。 */
    private fun isoUtc(epochMs: Long): String = java.time.Instant.ofEpochMilli(epochMs).toString()

    /** 从错误响应体解析原因码（优先 code，退 message，再退笼统）；供 400 拒绝告警可读（已脱敏，ADR-0057）。 */
    private fun parseErrorCode(jsonBody: String): String {
        val obj = JsonTree.asObject(codec.decode(jsonBody))
        return JsonTree.str(obj, "code") ?: JsonTree.str(obj, "message") ?: "请求被拒"
    }

    /** 把 BC 专属指标拼成 report 报文的 `proxy` 子对象（键名固定供控制面对齐，FR-34）。 */
    private fun proxyBody(proxy: ProxyMetrics): Map<String, Any?> =
        mapOf(
            "onlineConnections" to proxy.onlineConnections,
            "threadCount" to proxy.threadCount,
            "uptimeMs" to proxy.uptimeMs,
            "backendUp" to proxy.backendUp,
            "backendTotal" to proxy.backendTotal,
            "backendAvgLatencyMs" to proxy.backendAvgLatencyMs,
        )

    private fun appendParam(
        sb: StringBuilder,
        key: String,
        value: String?,
    ) {
        if (value.isNullOrEmpty()) return
        if (sb.isNotEmpty()) sb.append('&')
        sb.append(key).append('=').append(urlEncode(value))
    }

    private fun urlEncode(value: String): String {
        return java.net.URLEncoder.encode(value, Charsets.UTF_8.name())
    }

    private fun v2IdentityHeaders(identity: AgentIdentity): Map<String, String> {
        val h = LinkedHashMap<String, String>()
        h[HEADER_TOKEN] = settings.bootstrapToken
        h[HEADER_IDENTITY] = identity.identityId
        h[HEADER_BOOT] = identity.bootId
        return h
    }

    private fun v2Kind(role: String): String = if (role == "bungee") "proxy" else "backend"

    companion object {
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

    /** 连接级失败/其它非预期状态。 */
    data class Failed(val reason: String) : HeartbeatOutcome()
}
