package top.wcpe.beacon.agent.core.client

/**
 * 交付接口拉取结果（FR-269）：成功携清单；失败保留 HTTP 状态码与服务端错误码 / 说明，
 * **不再把所有非 200 折叠为 null**——控制面拒单原因（如 409 `config_artifact_missing`：配置渲染工件未就绪）
 * 必须能进回执与日志，运维与机器主体才知「为什么没成」。
 *
 * @param value      成功时拉到的清单；失败为 null
 * @param statusCode HTTP 状态码；连接级失败（请求未发出 / 无响应）为 [HTTP_NOT_SENT]
 * @param error      失败摘要（HTTP 码 + 错误码 + 说明，已脱敏、已截断）；成功为空串
 */
data class DeliveryFetchResult<out T>(
    val value: T?,
    val statusCode: Int,
    val error: String,
) {
    /** 是否拉到清单。 */
    val ok: Boolean get() = value != null
}

/** 连接级失败（请求压根没发出去 / 无响应）时占位的状态码。 */
const val HTTP_NOT_SENT: Int = 0
