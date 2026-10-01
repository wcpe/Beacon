package top.wcpe.beacon.agent.adapters

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.playerRoster
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.messaging.RosterDirectory

/**
 * [RosterDirectory] 的控制面 HTTP 适配器（FR-31）：把名册只读端口接到控制面 REST。
 *
 * **数据源**：控制面 `GET /beacon/v2/agent/player-roster`。名册权威在控制面（ADR-0063 决策 4）——控制面依连接
 * 明细（FR-145）在内存维护「玩家名 → 所在子服」快照，agent 侧不再持有 Redis 名册，本适配器只做一次只读取数。
 *
 * **隔离**：namespace 维度由**服务端**按鉴权身份（X-Beacon-Token + X-Beacon-Identity / X-Beacon-Boot）过滤，
 * 本适配器不传也不该传 namespace——归属是服务端权威，agent 侧无从自证，传给请求参数反而会开出一条越域的口子。
 *
 * **降级**（守不变量 #5，与 [top.wcpe.beacon.agent.core.messaging.RosterDirectoryHolder] 的语义同构）：
 * 控制面不可达 / 未注册（401）/ 5xx / 响应体非法 → 返空 Map，绝不抛、绝不返 null；业务插件据此走自身降级。
 * 读取失败不额外打日志：业务侧调用频次不可控（如每次 Tab 补全即读一次），逐次告警会刷爆日志；
 * 需要诊断时由壳层换用带日志的包装实现，本适配器只守「返空不抛」。
 *
 * **线程**：每调一次 [snapshot] 即一次同步阻塞 HTTP 往返（本适配器不缓存，陈旧度与调用频率由业务侧自行控制），
 * 须在异步线程调用——绝不上 MC 主线程（守不变量 #5）。
 *
 * @param apiClient 控制面 REST 客户端（与壳层其它数据面共用同一实例，共享连接池）
 * @param identity  本机 v2 身份（鉴权头真源）；须为**注册成功后**的身份，未注册时端点回 401 → 空名册
 */
class HttpRosterDirectory(
    private val apiClient: BeaconApiClient,
    private val identity: AgentIdentity,
) : RosterDirectory {
    /**
     * 读取全量名册快照（玩家名 → serverId）：取控制面本 namespace 名册。
     *
     * 任何失败（连接异常 / 非 200 / 响应不可解析）均已由客户端收成空 Map，此处再兜一层任何实现细节外抛的异常，
     * 守端口「绝不抛」契约——名册不可用绝不连累调用方。
     */
    override fun snapshot(): Map<String, String> =
        try {
            apiClient.playerRoster(identity)
        } catch (_: Exception) {
            emptyMap()
        }
}
