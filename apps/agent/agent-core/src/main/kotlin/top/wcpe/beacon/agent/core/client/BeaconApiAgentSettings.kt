package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.transport.HttpRequest

/**
 * 拉取控制面下发的 agent 运行配置（FR-277）：`GET /beacon/v2/agent/settings`。
 *
 * ## 为什么是普通请求而不是长轮询
 *
 * 配置下发的变更频率远低于业务配置（业务配置走 SSE 推送），且它的时效要求是「分钟级」
 * 而非「秒级」——为它单开一条挂起连接不划算。用普通请求 + 固定周期拉取，
 * 控制面无需为它维护 waiter，断了也只是晚一轮生效。
 *
 * ## 身份不可自报
 *
 * namespace / serverId 由服务端从鉴权中间件取，本方法**不传**任何身份参数：
 * 下发配置里有 `override.command-whitelist` 这类高危项，若读取权可伪造，
 * 任一 agent 就能读到别台服的运行参数。
 *
 * @return 成功返回 [AgentSettingsSnapshot]；失败返回 null（**不抛异常**：
 *         下发拉取失败不该影响 agent 主流程，继续用本地缓存即可）
 */
fun BeaconApiClient.pullAgentSettings(identity: AgentIdentity): AgentSettingsSnapshot? {
    val resp =
        exec(
            HttpRequest(
                method = "GET",
                url = "$base/beacon/v2/agent/settings",
                headers = headers(withBody = false, identity = identity),
                body = null,
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return null
    if (resp.statusCode != 200) {
        return null
    }
    return parseAgentSettings(resp.body)
}

/**
 * 一次拉取到的下发配置。
 *
 * @property values 键 → 值（服务端已剔除禁区键）
 * @property namespace 服务端认定的本机 namespace（agent 据此判断缓存是否仍属自己）
 * @property serverId 服务端认定的本机 serverId
 * @property rejectedKeys 服务端剔除的禁区键（agent 据此告警，让「Web 改了没生效」可见）
 */
data class AgentSettingsSnapshot(
    val values: Map<String, String>,
    val namespace: String,
    val serverId: String,
    val rejectedKeys: List<String>,
)

/**
 * 解析下发配置响应。
 *
 * 任何结构不符都返回 null（等价「这轮没拉到」）：下发配置是可选增强，
 * 解析失败时继续用本地缓存比让 agent 起不来更合理。
 */
private fun BeaconApiClient.parseAgentSettings(body: String): AgentSettingsSnapshot? {
    val decoded = codec.decode(body) ?: return null
    val root = JsonTree.asObject(decoded)
    return runCatching {
        AgentSettingsSnapshot(
            values = JsonTree.strMap(root, "values"),
            namespace = JsonTree.strOr(root, "namespace", ""),
            serverId = JsonTree.strOr(root, "serverId", ""),
            rejectedKeys = JsonTree.asList(root["rejectedKeys"]).mapNotNull { it as? String },
        )
    }.getOrNull()
}
