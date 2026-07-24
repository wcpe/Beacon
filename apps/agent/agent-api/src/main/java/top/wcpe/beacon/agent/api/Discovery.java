package top.wcpe.beacon.agent.api;

import java.util.List;
import java.util.Map;

/**
 * 服务发现查询。
 *
 * <p>背后走控制面 discovery 端点（同步 HTTP）。<b>请在异步线程调用</b>，
 * 不要在 MC 主线程使用。控制面不可用时抛底层异常或返回空，调用方自行降级。</p>
 */
public interface Discovery {

    /** 按条件过滤在线实例。 */
    List<ServiceInstance> query(DiscoveryQuery query);

    /** 列出某 zone 下的在线实例（拓扑视角便捷方法）。 */
    List<ServiceInstance> instancesInZone(String group, String zone);

    /** 列出某大区(group)下全部在线实例。 */
    List<ServiceInstance> instancesInGroup(String group);

    /**
     * 订阅拓扑变更（FR-29）：同 namespace 内实例上线 / 下线 / 改派 zone 时回调 listener。
     *
     * <p>复用 agent 既有的 server→agent 推送流，<b>不新建连接</b>；事件仅为「拓扑变了」通知，
     * 业务侧在回调里用 {@link #query} 重查最新拓扑。返回可注销句柄。</p>
     *
     * <p><b>回退</b>：agent 未启用推送流（控制面不可用 / 迁移期）时返回的句柄不可用、listener 不会触发；
     * 调用方应辅以周期 {@link #query} 兜底。</p>
     *
     * @return 可注销句柄（{@link ListenerHandle#remove()} 后不再回调）
     */
    ListenerHandle watch(TopologyListener listener);

    /**
     * 全量玩家位置名册（FR-31）：返回 玩家名 → 所在 serverId 的只读快照。
     * 名册取自单一全局 Redis hash（{@code beacon:player-loc}），不按 namespace 分区，
     * 单 BC 前提下即全量名册（见 ADR-0022）。
     *
     * <p>数据源是已躺在 agent 侧 Redis 的名册（由 BC 上的 beacon-proxy 维护，见 ADR-0022），
     * 供业务插件做总览 / 人数统计 / Tab 补全等只读消费。<b>名册最终一致</b>：换服瞬间可能短暂错位，
     * 业务侧须容忍瞬时偏差。</p>
     *
     * <p>同步、走 Redis IO，<b>请在异步线程调用</b>，不要在 MC 主线程使用。
     * 名册不可用（消息模块未开 / Redis 未连 / 名册为空）时返回<b>空 Map</b>（非 null、不抛异常）。</p>
     *
     * @return 玩家名 → serverId 的只读快照；不可用时为空 Map
     */
    Map<String, String> roster();

    /**
     * zone 过滤后的玩家位置名册（FR-31）：仅含落在该 group/zone 下子服的玩家。
     *
     * <p>zone 归属权威来自控制面 DB（ADR-0004）：先经发现解出该 zone 的 serverId 集合，再取全量名册中
     * value 落在该集合内的条目。<b>名册本身不携带 zone</b>，zone 维度只由发现结果反查（ADR-0022）。</p>
     *
     * <p>同步、走 Redis IO，<b>请在异步线程调用</b>，不要在 MC 主线程使用。
     * 名册不可用 / 该 zone 无人 / 交集为空时返回<b>空 Map</b>（非 null、不抛异常）。</p>
     *
     * @param group 大区
     * @param zone  分区
     * @return 该 zone 内 玩家名 → serverId 的只读快照；不可用或无人时为空 Map
     */
    Map<String, String> rosterInZone(String group, String zone);
}
