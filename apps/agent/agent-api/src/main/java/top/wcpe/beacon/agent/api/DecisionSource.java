package top.wcpe.beacon.agent.api;

/**
 * 一次调度决策的来源（FR-148）。
 *
 * <p>业务插件据此判断本次候选是控制面权威决策还是降级期本地兜底，用于日志 / 观测，不影响调用方式。</p>
 */
public enum DecisionSource {

    /** 控制面在线决策（正常路径，traceId 为服务端 traceId）。 */
    CONTROL_PLANE,

    /** 控制面不可用时 agent 依本地候选快照做的降级决策（fail-static，traceId 为本地 traceId）。 */
    LOCAL_FALLBACK
}
