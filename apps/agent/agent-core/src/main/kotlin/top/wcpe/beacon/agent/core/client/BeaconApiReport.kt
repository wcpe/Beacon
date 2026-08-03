package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.command.AssetEntry
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.metrics.ProxyMetrics
import top.wcpe.beacon.agent.core.sampling.BackendBatch
import top.wcpe.beacon.agent.core.sampling.MetricBatch
import top.wcpe.beacon.agent.core.sampling.MetricKind
import top.wcpe.beacon.agent.core.sampling.MetricSample
import top.wcpe.beacon.agent.core.sampling.ProxyBatch
import top.wcpe.beacon.agent.core.transport.HttpRequest

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
fun BeaconApiClient.report(
    identity: AgentIdentity,
    appliedMd5: String,
    health: HealthMetrics,
    backends: List<String> = emptyList(),
    proxy: ProxyMetrics? = null,
): Boolean {
    val body =
        buildMap {
            put("namespace", identity.namespace)
            put("serverId", identity.serverId)
            put("appliedMd5", appliedMd5)
            put("playerCount", health.playerCount)
            put("tps", health.tps)
            // 新增：JVM 已用 / 最大堆字节与进程 CPU 负载（键名固定供控制面对齐）。
            put("memUsed", health.memUsed)
            put("memMax", health.memMax)
            put("cpuLoad", health.cpuLoad)
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
fun BeaconApiClient.reportMetricsBatch(
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
fun BeaconApiClient.reportAssetManifest(
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
 * 把一个 5s 批聚合行拼成 v2 指标上报 samples 元素（FR-144）。
 *
 * 线上键与信封同为 **camelCase**（v2 API 通用约定；控制面接收结构体 json tag 亦 camelCase，再映射到 §3.1 的
 * snake_case DB 列——§3.1 是库表列名、非线上键）。每行含全部指标维度，不适用维度写缺省值（数值 0、后端 RTT -1），
 * 由控制面按 kind 解释，不特判 NULL。
 */
internal fun BeaconApiClient.metricBatchBody(batch: MetricBatch): Map<String, Any?> {
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
internal fun BeaconApiClient.parseMetricsAccepted(
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
internal fun BeaconApiClient.assetEntryBody(entry: AssetEntry): Map<String, Any?> =
    mapOf(
        "path" to entry.path,
        "sha256" to entry.sha256,
        "size" to entry.size,
        "mtimeMs" to entry.mtimeMs,
        "isText" to entry.isText,
    )

/** 解析文件资产清单 200 受理响应（应用后清单摘要 + 文件数）。 */
internal fun BeaconApiClient.parseAssetManifestAccepted(jsonBody: String): AssetManifestOutcome.Accepted {
    val obj = JsonTree.asObject(codec.decode(jsonBody))
    return AssetManifestOutcome.Accepted(
        digest = JsonTree.strOr(obj, "digest", ""),
        fileCount = JsonTree.intOr(obj, "fileCount", 0),
    )
}

/** 解析 report-local 202 响应（accepted / deduplicated 计数）。 */
internal fun BeaconApiClient.parseReportLocal(jsonBody: String): SchedReportLocalOutcome.Accepted {
    val obj = JsonTree.asObject(codec.decode(jsonBody))
    return SchedReportLocalOutcome.Accepted(
        accepted = JsonTree.intOr(obj, "accepted", 0),
        deduplicated = JsonTree.intOr(obj, "deduplicated", 0),
    )
}

/** 把一条本地决策补报记录拼成 report-local 的 decisions 元素（全 camelCase，可空字段空串占位）。 */
internal fun BeaconApiClient.localDecisionBody(report: LocalDecisionReport): Map<String, Any?> =
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

/** 把 BC 专属指标拼成 report 报文的 `proxy` 子对象（键名固定供控制面对齐，FR-34）。 */
internal fun BeaconApiClient.proxyBody(proxy: ProxyMetrics): Map<String, Any?> =
    mapOf(
        "onlineConnections" to proxy.onlineConnections,
        "threadCount" to proxy.threadCount,
        "uptimeMs" to proxy.uptimeMs,
        "backendUp" to proxy.backendUp,
        "backendTotal" to proxy.backendTotal,
        "backendAvgLatencyMs" to proxy.backendAvgLatencyMs,
    )
