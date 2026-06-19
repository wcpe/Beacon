package top.wcpe.beacon.agent.core.messaging

/**
 * 跨服消息信封（ADR-0016 决策 13：带 type + version，演进「只增不改」）。
 *
 * 信封与具体序列化库解耦：core 不引入 @Serializable，用 [toMap] / [fromMap] 在
 * 泛型树（Map<String,Any?>）与信封间互转，由 JsonCodec（适配器）落地 json 文本。
 *
 * 字段：
 * - [type]          业务消息类型，决定走哪条 on(type) 处理器；演进只增不改。
 * - [version]       信封版本号，向后兼容判据（新老插件混跑）。
 * - [payload]       与内容无关的业务负载（泛型树：Map/List/基本类型/null）。
 * - [correlationId] RPC 关联 ID：请求与回信据此配对；非 RPC 为 null。
 * - [replyTo]       RPC 回信通道：发起方专属回信地址；目标处理后回发到此；非 RPC 为 null。
 * - [source]        发起方 serverId，便于目标识别来源（可空）。
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
) {

    /** 是否为 RPC 请求（带回信通道 + 关联 ID）。 */
    fun isRequest(): Boolean = correlationId != null && replyTo != null

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
            )
        }
    }
}
