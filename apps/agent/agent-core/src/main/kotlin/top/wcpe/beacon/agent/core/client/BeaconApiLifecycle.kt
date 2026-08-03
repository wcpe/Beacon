package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.config.ConfigItem
import top.wcpe.beacon.agent.core.config.EffectiveResult
import top.wcpe.beacon.agent.core.connection.ConnectionEvent
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.transport.HttpRequest

/** 心跳：POST /beacon/v1/agent/heartbeat。404 → 需重新注册。 */
fun BeaconApiClient.heartbeat(identity: AgentIdentity): HeartbeatOutcome {
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
        403, 409 -> HeartbeatOutcome.AuthorityRefreshRequired
        else -> HeartbeatOutcome.Failed("非预期状态码 ${resp.statusCode}")
    }
}

/** 长轮询有效配置：GET /beacon/v1/agent/config/effective。 */
fun BeaconApiClient.pollEffective(
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
 * 连接明细批上报：POST /beacon/v2/agent/connections/batch（FR-145 §5.1，proxy 专用）。同步调用，请在异步线程使用。
 *
 * 报文 `{bootId, droppedCount, events[]}`；events 为 open/close 混合（单批 ≤500，全 camelCase 键），
 * 时间字段上线为 UTC ISO8601。202 受理（accepted / duplicated 计数）；429 队列忙、403 未确认、其它失败——
 * 均保留缓冲由上报循环重试（仅 202 才 ack 移除已上报事件）。
 */
fun BeaconApiClient.reportConnectionsBatch(
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

internal fun BeaconApiClient.parseEffective(jsonBody: String): EffectiveResult {
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

/** 把一条连接事件拼成 batch 报文的 events 元素（全 camelCase，空可选字段省略；时间 UTC ISO8601）。 */
internal fun BeaconApiClient.connectionEventBody(event: ConnectionEvent): Map<String, Any?> =
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
internal fun BeaconApiClient.parseConnectionsAccepted(jsonBody: String): ConnectionsReportOutcome.Accepted {
    val obj = JsonTree.asObject(codec.decode(jsonBody))
    return ConnectionsReportOutcome.Accepted(
        accepted = JsonTree.intOr(obj, "accepted", 0),
        duplicated = JsonTree.intOr(obj, "duplicated", 0),
    )
}

internal fun BeaconApiClient.parseActiveBinding(jsonBody: String): ActiveBinding? {
    val obj = JsonTree.asObject(codec.decode(jsonBody))
    val namespace = JsonTree.strOr(obj, "namespace", "")
    val serverId = JsonTree.strOr(obj, "serverId", "")
    val boundAt = JsonTree.strOr(obj, "boundAt", "")
    val fingerprint = JsonTree.strOr(obj, "bindingFingerprint", "")
    val compatAddress = JsonTree.strOr(obj, "address", JsonTree.strOr(obj, "addr", ""))
    val identityIncomplete = namespace.isBlank() || serverId.isBlank() || boundAt.isBlank() || compatAddress.isBlank()
    return if (identityIncomplete || !BeaconApiClient.FINGERPRINT.matches(fingerprint)) {
        null
    } else {
        ActiveBinding(namespace, serverId, boundAt, fingerprint, compatAddress)
    }
}
