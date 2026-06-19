package top.wcpe.beacon.agent.api;

import java.util.List;

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
}
