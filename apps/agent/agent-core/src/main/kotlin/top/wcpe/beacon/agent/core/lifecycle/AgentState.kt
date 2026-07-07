package top.wcpe.beacon.agent.core.lifecycle

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

    /**
     * 主动下线：被控制面主动下线（FR-49），注册被 INSTANCE_OFFLINE_REJECTED 拒绝。
     *
     * 与 DEGRADED（控制面不可用）严格区分：此态不退避猛打、不刷错误日志，
     * 改按大间隔降频探测重注册；取消下线后探测成功即回 RUNNING。仍 fail-static——按本地快照继续、不阻断玩家。
     */
    OFFLINE,
}
