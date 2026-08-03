package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.transport.HttpRequest

/**
 * 跨服消息上行发送：POST /beacon/v2/agent/messages/send（FR-149 §5.1，ADR-0063）。同步调用，请在异步线程使用。
 *
 * 报文 `{messageId, msgType, targetKind, targetServerId?|targetPlayerUuid?|targetZone?, correlationId?, payload, sentAt}`
 * （全 camelCase，sentAt 为 UTC ISO8601；targetZone 仅广播 zone 级定向时携带，FR-180）。
 * 200 受理（回报 status）；403 跨域无信任、400 payload 超限等被拒、其它失败。
 */
fun BeaconApiClient.sendMessage(
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
fun BeaconApiClient.pollMessages(
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
fun BeaconApiClient.ackMessages(
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

/** 解析消息发送 200 响应（messageId + status；messageId 缺失回退本地生成值）。 */
internal fun BeaconApiClient.parseMessageSent(
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
internal fun BeaconApiClient.parsePolledMessages(jsonBody: String): List<PolledMessage> {
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
internal fun BeaconApiClient.messageAckBody(ack: MessageAck): Map<String, Any?> =
    buildMap {
        put("messageId", ack.messageId)
        put("status", ack.status)
        if (ack.reason != null) put("reason", ack.reason)
        put("deliveredAt", isoUtc(ack.deliveredAtMs))
        if (ack.handlerCostMs != null) put("handlerCostMs", ack.handlerCostMs)
    }

/** 解析回执 200 响应（applied / ignored 计数）。 */
internal fun BeaconApiClient.parseMessageAckApplied(jsonBody: String): MessageAckOutcome.Applied {
    val obj = JsonTree.asObject(codec.decode(jsonBody))
    return MessageAckOutcome.Applied(
        applied = JsonTree.intOr(obj, "applied", 0),
        ignored = JsonTree.intOr(obj, "ignored", 0),
    )
}
