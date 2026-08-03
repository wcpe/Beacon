package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.identity.AgentIdentity

// ===== 顶层工具函数（从 BeaconApiClient 提取，不占类方法数） =====

internal fun appendEndpointReport(
    body: MutableMap<String, Any?>,
    identity: AgentIdentity,
) {
    if (identity.role == "bungee") {
        body["listeners"] =
            identity.endpointReport.proxyListenersForReport().map { listener ->
                mapOf("bindHost" to listener.bindHost, "port" to listener.port, "ordinal" to listener.ordinal)
            }
        return
    }
    body["listenPort"] = identity.endpointReport.backendPortForReport()
}

/** Unix 毫秒 → UTC ISO8601（如 2026-07-06T08:00:00.123Z），与控制面时间口径一致。 */
internal fun isoUtc(epochMs: Long): String = java.time.Instant.ofEpochMilli(epochMs).toString()

internal fun appendParam(
    sb: StringBuilder,
    key: String,
    value: String?,
) {
    if (value.isNullOrEmpty()) return
    if (sb.isNotEmpty()) sb.append('&')
    sb.append(key).append('=').append(urlEncode(value))
}

internal fun urlEncode(value: String): String {
    return java.net.URLEncoder.encode(value, Charsets.UTF_8.name())
}

internal fun v2Kind(role: String): String = if (role == "bungee") "proxy" else "backend"
