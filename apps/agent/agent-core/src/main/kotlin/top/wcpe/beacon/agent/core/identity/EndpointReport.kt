package top.wcpe.beacon.agent.core.identity

/** 平台壳层采集并一次性传入的监听端点事实，core 不依赖 Bukkit 或 Bungee API。 */
data class EndpointReport(
    val backendListenPort: Int? = null,
    val proxyListeners: List<ProxyListenerEndpoint> = emptyList(),
) {
    /** v2 端点契约必须显式发送端口；缺失以 0 交给控制面拒绝。 */
    fun backendPortForReport(): Int = backendListenPort ?: INVALID_PORT

    /** v2 端点契约必须保留所有上报 listener；无效项由控制面整体拒绝。 */
    fun proxyListenersForReport(): List<ProxyListenerEndpoint> = proxyListeners.sortedBy { it.ordinal }

    private companion object {
        const val INVALID_PORT = 0
    }
}

/** BC listener 的稳定顺序端点；bindHost 可为通配符，不从代理头推断。 */
data class ProxyListenerEndpoint(
    val bindHost: String,
    val port: Int,
    val ordinal: Int,
)
