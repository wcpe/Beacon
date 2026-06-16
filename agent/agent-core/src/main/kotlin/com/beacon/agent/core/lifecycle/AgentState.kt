package com.beacon.agent.core.lifecycle

/** agent 生命周期状态。 */
enum class AgentState {
    /** 启动中：读快照点亮有效配置。 */
    BOOTSTRAP,

    /** 注册中。 */
    REGISTERING,

    /** 运行中：心跳 + 长轮询双循环。 */
    RUNNING,

    /** 降级：控制面不可用，按本地快照继续，退避重连。 */
    DEGRADED,
}
