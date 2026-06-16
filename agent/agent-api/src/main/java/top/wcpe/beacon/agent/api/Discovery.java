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
}
