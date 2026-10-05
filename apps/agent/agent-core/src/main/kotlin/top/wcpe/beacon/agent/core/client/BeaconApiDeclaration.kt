package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.api.DeclarationApplied
import top.wcpe.beacon.agent.api.NodeDeclaration
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.transport.HttpRequest

/** 节点自声明（运行期刷新容量 / 标签）端点：POST /beacon/v1/agent/declaration（FR-243，见 ADR-0086）。 */
private const val DECLARATION_PATH = "/beacon/v1/agent/declaration"

/**
 * 节点自声明：刷新**本节点**的容量与 / 或自定义标签。
 *
 * 报文 `{namespace, serverId, capacity?, labels?}`：身份两键只用于定位自己（身份与在册归属以控制面
 * 中间件 + 在册判定为准）；`capacity` 缺键 = 不刷新该字段（0 是合法值，与缺键区分）；`labels` 缺键 =
 * 不刷新，提供即**整体替换**（空对象 = 清空全部标签）。
 *
 * 结果映射（真源见规格 §3.4）：`200 → Applied`（回带生效值）、`400 → Rejected`（超界 / 格式非法）、
 * `401 / 404 → Unavailable`（token 缺错 / 实例不在册）、连接级失败或其它非预期状态码 → `Unavailable`
 * （可重试；非预期码绝不当成「被拒」这一稳定事实）。
 *
 * 同步 HTTP，仅在异步线程调用（绝不在 MC 主线程使用）。
 */
fun BeaconApiClient.declare(
    identity: AgentIdentity,
    declaration: NodeDeclaration,
): DeclarationResult {
    val body =
        buildMap {
            put("namespace", identity.namespace)
            put("serverId", identity.serverId)
            // 缺键 = 不刷新：只在本字段确有值时拼入（capacity 须用 orElse(null) 取，勿用 0 兜底）。
            val capacity: Int? = declaration.capacity().orElse(null)
            if (capacity != null) put("capacity", capacity)
            if (declaration.hasLabels()) put("labels", declaration.labels())
        }
    val resp =
        exec(
            HttpRequest(
                method = "POST",
                url = "$base$DECLARATION_PATH",
                headers = headers(withBody = true, identity = identity),
                body = codec.encode(body),
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return DeclarationResult.Unavailable(connectFailReason())

    return when (resp.statusCode) {
        // 200：控制面已刷新并回带生效值（自证），据此构造生效值返回。
        200 -> DeclarationResult.Applied(parseAppliedDeclaration(resp.body))
        400 -> DeclarationResult.Rejected(parseErrorCode(resp.body))
        // 401 token 缺 / 错，404 尚未挂载数据面（未注册 / 已归档）：均属「通道不可用」，可退避后重报。
        401 -> DeclarationResult.Unavailable("控制面拒绝鉴权（401），请检查 bootstrap token")
        404 -> DeclarationResult.Unavailable("实例不在册（404），注册成功后再重报")
        else -> DeclarationResult.Unavailable("非预期状态码 ${resp.statusCode}")
    }
}

/** 解析 200 响应里的生效值 `{ok, capacity, labels}`；缺键即视为控制面未回带该字段。 */
internal fun BeaconApiClient.parseAppliedDeclaration(jsonBody: String): DeclarationApplied {
    val obj = JsonTree.asObject(codec.decode(jsonBody))
    val capacity = (obj["capacity"] as? Number)?.toInt()
    val labels = JsonTree.asObject(obj["labels"]).mapValues { JsonTree.asString(it.value) }
    return DeclarationApplied(capacity, labels)
}
