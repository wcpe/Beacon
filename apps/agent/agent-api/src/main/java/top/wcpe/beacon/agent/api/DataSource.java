package top.wcpe.beacon.agent.api;

/**
 * 候选 / 健康视图当前的数据来源（FR-148）。
 *
 * <p>与 {@link DecisionSource} 区分：本枚举描述「本机缓存此刻从哪来」（周期刷新态），
 * {@link DecisionSource} 描述「某一次决策由谁做出」（单次决策态）。</p>
 */
public enum DataSource {

    /** 数据来自控制面在线刷新（最近一次候选刷新成功）。 */
    CONTROL_PLANE,

    /** 控制面不可用，数据来自最后一次落盘快照（含重启后恢复，fail-static）。 */
    LOCAL_SNAPSHOT
}
