package top.wcpe.beacon.agent.api;

/**
 * 拓扑变更回调（FR-29）。
 *
 * <p>当同 namespace 内任一实例上线 / 下线 / 改派 zone 时触发——这是「拓扑变了」的<b>通知信号</b>，
 * 不携带实例数据；收到后业务侧自行用 {@link Discovery#query} 重查最新拓扑。</p>
 *
 * <p>注意：回调在 agent 异步线程触发，重活请自行切到业务线程。控制面不可用 / 未启用推送流时不会触发
 * （订阅句柄不可用，见 {@link Discovery#watch}）。</p>
 */
public interface TopologyListener {

    /** 拓扑发生变更（上线 / 下线 / 改派）；业务侧据此重查发现端点。 */
    void onTopologyChanged();
}
