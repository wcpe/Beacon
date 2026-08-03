package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.transport.HttpRequest

/**
 * 服务发现：GET /beacon/v1/agent/discovery。同步调用，请在异步线程使用。
 *
 * 传 null 的过滤维度不拼入查询；tags 以 tag.<key>=<value> 形式拼入（多 tag 取交集，FR-29）。
 * 返回可用实例（online+degraded）的泛型树列表（由调用方映射为 API 值对象）。
 */
fun BeaconApiClient.discover(
    namespace: String?,
    group: String?,
    zone: String?,
    role: String?,
    tags: Map<String, String> = emptyMap(),
): List<Map<String, Any?>> = discover(DiscoveryFilters(namespace = namespace, group = group, zone = zone, role = role, tags = tags))

fun BeaconApiClient.discover(
    filters: DiscoveryFilters,
    identity: AgentIdentity? = null,
): List<Map<String, Any?>> =
    when (val result = discoverResult(filters, identity)) {
        is DiscoveryFetchResult.Success -> result.instances
        is DiscoveryFetchResult.Failed -> emptyList()
    }

/**
 * 显式区分权威成功快照与发现失败，供需要 fail-static 的内部同步器消费。
 * 旧 [discover] 仍把失败安全降级为空列表，保持公开 Discovery.query() 既有语义。
 */
fun BeaconApiClient.discoverResult(
    filters: DiscoveryFilters,
    identity: AgentIdentity? = null,
): DiscoveryFetchResult<Map<String, Any?>> {
    val params = StringBuilder()
    appendParam(params, "namespace", filters.namespace)
    appendParam(params, "group", filters.group)
    appendParam(params, "zone", filters.zone)
    appendParam(params, "role", filters.role)
    for ((key, value) in filters.tags) {
        appendParam(params, "tag.$key", value)
    }
    val url = "$base/beacon/v1/agent/discovery" + if (params.isEmpty()) "" else "?$params"
    val resp =
        exec(
            HttpRequest(
                method = "GET",
                url = url,
                headers = headers(withBody = false, identity = identity),
                body = null,
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        )
    return when {
        resp == null -> DiscoveryFetchResult.Failed(BeaconApiClient.DISCOVERY_REQUEST_FAILED)
        resp.statusCode != 200 -> DiscoveryFetchResult.Failed("非预期状态码 ${resp.statusCode}")
        else -> parseDiscoveryResponse(resp.body)
    }
}

internal fun BeaconApiClient.parseDiscoveryResponse(body: String): DiscoveryFetchResult<Map<String, Any?>> =
    try {
        parseDecodedDiscovery(codec.decode(body))
    } catch (_: Exception) {
        DiscoveryFetchResult.Failed(BeaconApiClient.DISCOVERY_DECODE_FAILED)
    }

internal fun BeaconApiClient.parseDecodedDiscovery(decoded: Any?): DiscoveryFetchResult<Map<String, Any?>> {
    val response = strictJsonObject(decoded) ?: return DiscoveryFetchResult.Failed(BeaconApiClient.DISCOVERY_STRUCTURE_INVALID)
    if (!response.containsKey("instances")) return DiscoveryFetchResult.Failed(BeaconApiClient.DISCOVERY_STRUCTURE_INVALID)
    val rawInstances = response["instances"] as? List<*> ?: return DiscoveryFetchResult.Failed(BeaconApiClient.DISCOVERY_STRUCTURE_INVALID)
    val instances = ArrayList<Map<String, Any?>>(rawInstances.size)
    for (rawInstance in rawInstances) {
        val instance = strictJsonObject(rawInstance) ?: return DiscoveryFetchResult.Failed(BeaconApiClient.DISCOVERY_STRUCTURE_INVALID)
        instances.add(instance)
    }
    return DiscoveryFetchResult.Success(instances)
}

@Suppress("UNCHECKED_CAST")
internal fun BeaconApiClient.strictJsonObject(value: Any?): Map<String, Any?>? {
    val obj = value as? Map<*, *> ?: return null
    if (obj.keys.any { it !is String }) return null
    return obj as Map<String, Any?>
}
