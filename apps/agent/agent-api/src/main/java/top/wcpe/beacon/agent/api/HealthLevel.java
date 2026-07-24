package top.wcpe.beacon.agent.api;

/**
 * 健康等级（FR-147）。
 *
 * <p>与控制面线上小写值 {@code healthy} / {@code degraded} / {@code unhealthy} 一一对应；
 * 线上字符串到本枚举的映射只发生在 agent 适配器内，业务插件只见强类型枚举。</p>
 */
public enum HealthLevel {

    /** 健康：score ≥ healthyMin，正常可调度。 */
    HEALTHY,

    /** 亚健康：degradedMin ≤ score &lt; healthyMin，仍可调度、仅排序靠后。 */
    DEGRADED,

    /** 不健康：score &lt; degradedMin，不可调度。 */
    UNHEALTHY
}
