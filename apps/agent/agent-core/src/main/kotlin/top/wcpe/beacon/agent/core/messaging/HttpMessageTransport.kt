package top.wcpe.beacon.agent.core.messaging

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.MessageSendOutcome
import top.wcpe.beacon.agent.core.client.OutboundMessage
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.transport.JsonCodec

/**
 * 跨服消息 HTTP 中转传输（ADR-0063 取代 [RedisMessageTransport][top.wcpe.beacon.agent.adapters.messaging.RedisMessageTransport]）：
 * 把 [MessageBus] 的出站原语映射为控制面 REST 上行。
 *
 * - 上行：sendToServer / sendReply → 解信封 → [BeaconApiClient.sendMessage]（据信封 targetKind 建 server/player wire 目标）。
 * - 入站不经此：HTTP 单通道由 [MessagePollCoordinator] 长轮询取回后直调 [MessageBus.deliverInbound]，
 *   故 subscribeServerInbox / subscribeReplyInbox 只满足接口、不驱动。
 * - publishTopic / subscribeTopic / unsubscribeTopic 底层 no-op（ADR-0063 §7，v2 不做 topic）。
 *
 * 守 ADR-0005：不 import okhttp / kotlinx，只依赖 [BeaconApiClient]（其 HTTP/JSON 在适配器）与 [JsonCodec] 接口。
 *
 * @param apiClient 控制面 REST 客户端
 * @param identity  本机身份（鉴权头）
 * @param codec     信封编解码（解出站 rawJson 取结构字段）
 * @param warn      WARN 日志（发送被拒 / 失败）
 */
class HttpMessageTransport(
    private val apiClient: BeaconApiClient,
    private val identity: AgentIdentity,
    private val codec: JsonCodec,
    private val warn: (String) -> Unit = {},
) : MessageTransport {
    @Volatile
    private var connected = false

    override fun start() {
        connected = true
    }

    override fun close() {
        connected = false
    }

    override fun isConnected(): Boolean = connected

    /**
     * 上行发送：解信封 → 组 wire → [BeaconApiClient.sendMessage]。解码失败 / 缺 messageId 丢弃。
     * 失败仅告警，不本地缓冲重发（ADR-0063 §8）。目标从信封 targetKind/targetId 取（serverId 参数在 HTTP 下不用）。
     */
    override fun sendToServer(
        serverId: String,
        rawJson: String,
    ) {
        val message =
            try {
                Message.fromMap(codec.decode(rawJson))
            } catch (t: Throwable) {
                warn("出站信封解码失败：${t.message}")
                null
            } ?: return
        val messageId = message.messageId
        if (messageId == null) {
            warn("出站消息缺 messageId，丢弃 type=${message.type}")
            return
        }
        val kind = message.targetKind ?: Message.TARGET_SERVER
        val outbound =
            OutboundMessage(
                messageId = messageId,
                msgType = message.type,
                targetKind = kind,
                targetServerId = if (kind == Message.TARGET_SERVER) message.targetId else null,
                targetPlayerUuid = if (kind == Message.TARGET_PLAYER) message.targetId else null,
                correlationId = message.correlationId,
                payload = message.payload,
                sentAtMs = message.sentAt ?: System.currentTimeMillis(),
            )
        when (val outcome = apiClient.sendMessage(identity, outbound)) {
            is MessageSendOutcome.Ok -> Unit
            is MessageSendOutcome.Forbidden ->
                warn("跨服消息被拒：跨 namespace 无信任，type=${message.type} target=${message.targetId}")

            is MessageSendOutcome.Rejected ->
                warn("跨服消息被拒：${outcome.reason}，type=${message.type}")

            is MessageSendOutcome.Failed ->
                warn("跨服消息发送失败：${outcome.reason}，type=${message.type}（实时消息不本地缓冲重发）")
        }
    }

    /** HTTP 回信亦是一条定向消息（目标在信封 targetId），与 sendToServer 同路；实际 reply 走 source 定向、通常不经此。 */
    override fun sendReply(
        replyChannel: String,
        rawJson: String,
    ) = sendToServer(replyChannel, rawJson)

    override fun publishTopic(
        topic: String,
        rawJson: String,
    ) = Unit

    override fun subscribeServerInbox(onMessage: (String) -> Unit) = Unit

    override fun subscribeReplyInbox(
        replyChannel: String,
        onMessage: (String) -> Unit,
    ) = Unit

    override fun subscribeTopic(
        topic: String,
        onMessage: (String) -> Unit,
    ) = Unit

    override fun unsubscribeTopic(topic: String) = Unit
}
