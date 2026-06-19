package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.ListenerHandle
import top.wcpe.beacon.agent.api.TopologyListener
import java.util.concurrent.ConcurrentHashMap

/**
 * 拓扑 watch 监听器表（FR-29）：DiscoveryView.watch 注册到此，AgentLifecycle 收到 topology-changed
 * 事件后调用 [fireTopologyChanged] 扇出回调。
 *
 * 与 EffectiveConfigView 的监听器表同构：以句柄对象自身为键便于注销，回调在 agent 异步线程触发。
 * 不在事件里搬实例数据——回调仅为"拓扑变了"通知，业务侧自行重查发现端点（守控制面/数据面边界）。
 */
class TopologyWatchHub {

    // 监听器表：以句柄对象自身为键，便于注销。
    private val listeners = ConcurrentHashMap<Any, TopologyListener>()

    /** 注册拓扑监听器，返回可注销句柄。 */
    fun watch(listener: TopologyListener): ListenerHandle {
        val key = Any()
        listeners[key] = listener
        return ListenerHandle { listeners.remove(key) }
    }

    /** 由 AgentLifecycle 在收到 topology-changed 事件后调用，回调所有监听器。 */
    fun fireTopologyChanged() {
        for (listener in listeners.values) {
            listener.onTopologyChanged()
        }
    }
}
