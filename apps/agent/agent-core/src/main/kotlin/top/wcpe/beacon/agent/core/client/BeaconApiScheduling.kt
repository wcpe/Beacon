package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.transport.HttpRequest

/**
 * 拉取调度候选快照：GET /beacon/v2/agent/schedule/candidates（FR-148 §5.1）。同步调用，请在异步线程使用。
 *
 * 无参（服务端按请求方 namespace 圈定）；200 返回 `{generatedAtMs, zones:[{zone, candidates:[...]}]}`
 * （仅 schedulable / degraded 候选）。连接失败 / 非 200 → Failed（本轮放弃刷新，沿用上一快照）。
 */
fun BeaconApiClient.scheduleCandidates(identity: AgentIdentity): SchedCandidatesOutcome {
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
 * [BeaconApiClient.SCHED_DECIDE_TIMEOUT_MS]（玩家链路不容久等，超时即由上层降级本地决策）。200 决策成功 / 无候选；
 * 404 zone_not_found；403 cross_namespace；400 参数非法；其它 / 连接失败 → Failed（触发降级）。
 */
fun BeaconApiClient.scheduleDecide(
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
                readTimeoutMs = BeaconApiClient.SCHED_DECIDE_TIMEOUT_MS,
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
fun BeaconApiClient.reportLocalDecisions(
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

/** 解析 candidates 200 响应（generatedAtMs + 各 zone 候选 + 可选大厅候选）。 */
internal fun BeaconApiClient.parseCandidates(jsonBody: String): SchedCandidates {
    val obj = JsonTree.asObject(codec.decode(jsonBody))
    val zones =
        JsonTree.asList(obj["zones"]).map { rawZone ->
            val zoneObj = JsonTree.asObject(rawZone)
            ZoneCandidates(
                zone = JsonTree.strOr(zoneObj, "zone", ""),
                candidates =
                    JsonTree.asList(zoneObj["candidates"]).map { rawCand ->
                        val c = JsonTree.asObject(rawCand)
                        parseCandidate(c)
                    },
            )
        }
    val lobby =
        (obj["lobby"] as? Map<*, *>)?.let { rawLobby ->
            val lobbyObj = JsonTree.asObject(rawLobby)
            LobbyCandidates(
                clusterId = JsonTree.longOr(lobbyObj, "clusterId", 0L),
                ready = JsonTree.boolOr(lobbyObj, "ready", false),
                candidates = JsonTree.asList(lobbyObj["candidates"]).map { raw -> parseCandidate(JsonTree.asObject(raw)) },
            )
        }
    return SchedCandidates(generatedAtMs = JsonTree.longOr(obj, "generatedAtMs", 0L), zones = zones, lobby = lobby)
}

internal fun BeaconApiClient.parseCandidate(obj: Map<String, Any?>): CandidateEntry =
    CandidateEntry(
        serverId = JsonTree.strOr(obj, "serverId", ""),
        score = JsonTree.intOr(obj, "score", 0),
        level = JsonTree.strOr(obj, "level", ""),
        schedulable = JsonTree.boolOr(obj, "schedulable", true),
        onlineCount = JsonTree.intOr(obj, "onlineCount", 0),
        maxOnline = JsonTree.intOr(obj, "maxOnline", 0),
        reasons = JsonTree.asList(obj["reasons"]).map(JsonTree::asString),
    )

/** 解析 decide 200 响应（chosen 可空、failReason 可空）。 */
internal fun BeaconApiClient.parseDecide(jsonBody: String): SchedDecideOutcome.Decided {
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
