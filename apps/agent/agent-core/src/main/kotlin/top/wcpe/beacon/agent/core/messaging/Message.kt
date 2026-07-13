package top.wcpe.beacon.agent.core.messaging

/**
 * 跨服消息信封（ADR-0016 决策 13：带 type + version，演进「只增不改」；P5 起经控制面 HTTP 中转，见 ADR-0063）。
 *
 * 信封与具体序列化库解耦：core 不引入 @Serializable，用 [toMap] / [fromMap] 在
 * 泛型树（Map<String,Any?>）与信封间互转，由 JsonCodec（适配器）落地 json 文本。
 *
 * 字段：
 * - [type]          业务消息类型，决定走哪条 on(type) 处理器；演进只增不改。
 * - [version]       信封版本号，向后兼容判据（新老插件混跑）。
 * - [payload]       与内容无关的业务负载（泛型树：Map/List/基本类型/null）。
 * - [correlationId] RPC 关联 ID：RPC 请求填自身 messageId（自引用）、响应填请求的 messageId；非 RPC 为 null。
 * - [replyTo]       RPC 回信通道：仅 Redis 通道用（发起方回信地址）；HTTP 中转回信改按 [source] 定向，不依赖此。
 * - [source]        发起方 serverId，便于目标识别来源与 HTTP 中转回信定向（可空）。
 * - [messageId]     P5 起：源 agent 发送时生成的 UUIDv7（控制面据高 48 位时间戳定位日表）；旧 Redis 通道可空。
 * - [sentAt]        P5 起：发送时刻（Unix 毫秒）；上线经适配器格式化为 UTC ISO8601。
 * - [targetKind]    P5 起：寻址类型 `server` / `player` / `broadcast`（FR-180）；HTTP 中转据此建 wire 目标（Redis 通道忽略）。
 * - [targetId]      P5 起：目标标识（targetKind=server 为 serverId、player 为 playerUuid、broadcast 为可选 zone 名）。
 * - [broadcast]     FR-180：广播投递标记（additive 键，只增不改）。入站消息带 true 时按 topic 订阅分发表路由，
 *                   与定向 on(type) 分发表隔离；定向消息缺省 false。
 *
 * 不可变值对象。
 */
data class Message(
    val type: String,
    val payload: Any?,
    val version: Int = CURRENT_VERSION,
    val correlationId: String? = null,
    val replyTo: String? = null,
    val source: String? = null,
    val messageId: String? = null,
    val sentAt: Long? = null,
    val targetKind: String? = null,
    val targetId: String? = null,
    val broadcast: Boolean = false,
) {
    /**
     * 是否为需要回信的 RPC 请求。
     *
     * HTTP 中转下回信通道（replyTo）不随控制面转发投递到目标，故不能以 replyTo 判定；改以「带 correlationId」
     * 为判据：能抵达 on(type) 处理器的消息里，带 correlationId 者即 RPC 请求（响应在 [MessageBus] 分发时已被
     * correlationId 前置拦截、不会抵达处理器），单向 send 无 correlationId。
     */
    fun isRequest(): Boolean = correlationId != null

    /**
     * 转为泛型树（供 JsonCodec.encode）。
     *
     * 只放非 null 字段（演进只增不改：老读端遇到缺失的可选字段按默认处理）。
     */
    fun toMap(): Map<String, Any?> {
        val map = LinkedHashMap<String, Any?>()
        map[FIELD_TYPE] = type
        map[FIELD_VERSION] = version
        map[FIELD_PAYLOAD] = payload
        if (correlationId != null) map[FIELD_CORRELATION_ID] = correlationId
        if (replyTo != null) map[FIELD_REPLY_TO] = replyTo
        if (source != null) map[FIELD_SOURCE] = source
        if (messageId != null) map[FIELD_MESSAGE_ID] = messageId
        if (sentAt != null) map[FIELD_SENT_AT] = sentAt
        if (targetKind != null) map[FIELD_TARGET_KIND] = targetKind
        if (targetId != null) map[FIELD_TARGET_ID] = targetId
        if (broadcast) map[FIELD_BROADCAST] = true
        return map
    }

    companion object {
        /** 当前信封版本号。新增可选字段时不变；不兼容变更才升（本 FR 不预期发生）。 */
        const val CURRENT_VERSION: Int = 1

        const val FIELD_TYPE: String = "type"
        const val FIELD_VERSION: String = "version"
        const val FIELD_PAYLOAD: String = "payload"
        const val FIELD_CORRELATION_ID: String = "correlationId"
        const val FIELD_REPLY_TO: String = "replyTo"
        const val FIELD_SOURCE: String = "source"
        const val FIELD_MESSAGE_ID: String = "messageId"
        const val FIELD_SENT_AT: String = "sentAt"
        const val FIELD_TARGET_KIND: String = "targetKind"
        const val FIELD_TARGET_ID: String = "targetId"
        const val FIELD_BROADCAST: String = "broadcast"

        /** 寻址类型：定向到子服。 */
        const val TARGET_SERVER: String = "server"

        /** 寻址类型：按玩家所在服（控制面解析）。 */
        const val TARGET_PLAYER: String = "player"

        /** 寻址类型：广播 fan-out（控制面按当前在线服集合解析，FR-180 / ADR-0065）。 */
        const val TARGET_BROADCAST: String = "broadcast"

        /**
         * 从泛型树（JsonCodec.decode 的结果）还原信封。
         *
         * 缺 type 视为非法消息返回 null（调用方据此丢弃并告警）；缺 version 按当前版本兜底
         * （兼容老发送端未带 version 的极端情况，符合「只增不改」的宽进策略）。
         */
        @Suppress("UNCHECKED_CAST")
        fun fromMap(tree: Any?): Message? {
            val map = tree as? Map<String, Any?> ?: return null
            val type = map[FIELD_TYPE] as? String ?: return null
            val version = (map[FIELD_VERSION] as? Number)?.toInt() ?: CURRENT_VERSION
            return Message(
                type = type,
                payload = map[FIELD_PAYLOAD],
                version = version,
                correlationId = map[FIELD_CORRELATION_ID] as? String,
                replyTo = map[FIELD_REPLY_TO] as? String,
                source = map[FIELD_SOURCE] as? String,
                messageId = map[FIELD_MESSAGE_ID] as? String,
                sentAt = (map[FIELD_SENT_AT] as? Number)?.toLong(),
                targetKind = map[FIELD_TARGET_KIND] as? String,
                targetId = map[FIELD_TARGET_ID] as? String,
                broadcast = map[FIELD_BROADCAST] as? Boolean ?: false,
            )
        }
    }
}
