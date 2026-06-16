package top.wcpe.beacon.agent.core.lifecycle

/**
 * agent 生命周期对外可观测快照（供壳层 status 运维命令渲染）。
 *
 * core 侧值对象，不含平台类型——壳层取值后自行拼显示文案（守 ADR-0005）。
 *
 * @param state                当前生命周期状态
 * @param connected            是否已连上控制面（state==RUNNING）
 * @param effectiveMd5         当前有效配置整体 md5；尚无有效配置时为 null
 * @param heartbeatIntervalSec 当前心跳周期（秒）；注册前为兜底值换算
 * @param endpoint             当前选用的控制面基址
 */
data class LifecycleSnapshot(
    val state: AgentState,
    val connected: Boolean,
    val effectiveMd5: String?,
    val heartbeatIntervalSec: Int,
    val endpoint: String,
)
