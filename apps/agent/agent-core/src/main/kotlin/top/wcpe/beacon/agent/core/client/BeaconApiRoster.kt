package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.transport.HttpRequest

/**
 * 拉取在线玩家名册：GET /beacon/v2/agent/player-roster（FR-31）。同步调用，请在异步线程使用。
 *
 * 名册权威在控制面（ADR-0063 决策 4）：控制面依连接明细在内存维护「玩家名 → 所在子服」快照，agent 侧不再持有名册。
 * 本端点只返回**调用方所属 namespace** 的名册——namespace 隔离由服务端按鉴权身份保证，agent 侧不传 namespace 参数
 * （子服 agent 物理上看不到异服玩家，也无法自证归属，故不臆造该维度）。
 *
 * 鉴权头与既有 v2 agent 面端点（如 /beacon/v2/agent/schedule/candidates）同款：
 * X-Beacon-Token + X-Beacon-Identity / X-Beacon-Boot，故**须在身份注册成功之后调用**（未注册时服务端回 401）。
 *
 * 200 形如 `{"namespace":"<code>","count":2,"players":{"<玩家名>":"<serverId>"}}`，本方法只取 players 映射
 * （用户面只关心「谁在哪个服」，namespace / count 是服务端自述，不进业务语义）。
 *
 * **失败安全降级（守不变量 #5）**：连接失败 / 非 200（含 401 未注册、5xx）/ 响应体不可解析 → 返回空 Map，
 * 绝不返 null、绝不抛，与 [top.wcpe.beacon.agent.core.messaging.RosterDirectory] 端口契约同款降级。
 * 于是空 Map 既可表示「本 namespace 无人在线」，也可表示「名册不可用」——调用方按端口契约本就不区分二者，
 * 均走「名册为空」的降级分支。
 */
fun BeaconApiClient.playerRoster(identity: AgentIdentity): Map<String, String> {
    val resp =
        exec(
            HttpRequest(
                method = "GET",
                url = "$base/beacon/v2/agent/player-roster",
                headers = headers(withBody = false, identity = identity),
                body = null,
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return emptyMap()
    if (resp.statusCode != 200) return emptyMap()
    return parsePlayerRoster(resp.body)
}

/**
 * 解析名册 200 响应：取顶层 players 的「玩家名 → serverId」映射。
 *
 * 结构非法（响应体非合法 JSON / 顶层非对象 / 缺 players / players 非对象）→ 整体返空 Map（名册不可用）；
 * 单个条目值非字符串 → 丢该条目而非整体作废（一处脏数据不该让整份名册不可用）。
 */
internal fun BeaconApiClient.parsePlayerRoster(jsonBody: String): Map<String, String> {
    // 响应体不是合法 JSON 时按「结构非法」同路处理：降级为空对象，由下面的缺 players 分支统一返空。
    val obj =
        try {
            JsonTree.asObject(codec.decode(jsonBody))
        } catch (_: Exception) {
            emptyMap<String, Any?>()
        }
    val rawPlayers = obj["players"] as? Map<*, *> ?: return emptyMap()
    val players = LinkedHashMap<String, String>(rawPlayers.size)
    for ((name, serverId) in rawPlayers) {
        if (name is String && serverId is String) players[name] = serverId
    }
    return players
}
