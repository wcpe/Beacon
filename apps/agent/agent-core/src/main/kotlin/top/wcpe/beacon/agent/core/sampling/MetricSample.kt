package top.wcpe.beacon.agent.core.sampling

/**
 * 采样角色（FR-144，见 v2-metrics-health-scheduling.md §4.1）：proxy 与 backend 采样字段集不同。
 *
 * 由 agent 身份角色决定（bungee → PROXY，其余 → BACKEND），与 v2 注册 kind 对齐（proxy / backend）。
 */
enum class MetricKind(val wire: String) {
    /** 后端子服：有 TPS / 在线 / 容量，无连接 / 后端可达性。 */
    BACKEND("backend"),

    /** 代理：有连接数 / 后端可达性，无 TPS / 在线容量。 */
    PROXY("proxy"),
}

/**
 * 采样载荷（proxy / backend 差异部分，FR-144 §4.1）。
 *
 * 用密封类型建模 proxy 与 backend 的**互斥字段集**——backend 无 conn / 后端探测，proxy 无 tps / 在线容量，
 * 避免给不适用维度塞无意义的零值（贫血且易误读）。样本的 [MetricKind] 由载荷类型决定。
 */
sealed interface SamplePayload

/**
 * backend 专属采样字段（FR-144 §4.1）。
 *
 * @param tps         tick 计数滑动推算的 TPS（0~20），由主线程原子埋点推算（见壳层 tick 埋点）
 * @param onlineCount 在线玩家数（主线程 volatile 埋点，采样线程不直调线程不安全的 Bukkit API）
 * @param maxOnline   容量上限（取自身份 capacity）
 */
data class BackendSample(
    val tps: Double,
    val onlineCount: Int,
    val maxOnline: Int,
) : SamplePayload

/**
 * proxy 专属采样字段（FR-144 §4.1）。
 *
 * @param connCount        代理当前在线连接数
 * @param backendUp        可达后端子服数（TCP 探测成功计数）
 * @param backendTotal     配置的后端子服总数
 * @param backendAvgRttMs  可达后端 TCP RTT 均值（毫秒），无可达为 [RTT_UNAVAILABLE]（-1）
 */
data class ProxySample(
    val connCount: Int,
    val backendUp: Int,
    val backendTotal: Int,
    val backendAvgRttMs: Double,
) : SamplePayload

/**
 * 每秒一条的原始采样样本（FR-144 §4.1）。原始 1s 样本仅存在于 agent 环形缓冲与批内聚合，不落库
 * （落库的是 5s 批聚合行 [MetricBatch]，见 §8 待定 1）。
 *
 * 采样在 agent 独立采样线程执行，**绝不在 MC 主线程做采样或 IO**（架构不变量 §5）；
 * 主线程只承担零成本原子埋点（tick 自增 / online volatile），采样线程读原子值推算。
 *
 * @param tsMs        采样时刻（agent 本地时钟）
 * @param cpuPct      进程 CPU 使用率 %（[0,100]），不可用为 [CPU_UNAVAILABLE]（-1）
 * @param memUsedMb   已用堆内存 MB
 * @param memMaxMb    最大堆内存 MB
 * @param reportRttMs 上一批上报的 HTTP 往返毫秒，未知为 [RTT_UNAVAILABLE]（-1）
 * @param payload     proxy / backend 差异字段
 */
data class MetricSample(
    val tsMs: Long,
    val cpuPct: Double,
    val memUsedMb: Double,
    val memMaxMb: Int,
    val reportRttMs: Int,
    val payload: SamplePayload,
) {
    /** 由载荷类型推断采样角色（backend / proxy）。 */
    val kind: MetricKind
        get() = if (payload is ProxySample) MetricKind.PROXY else MetricKind.BACKEND

    companion object {
        /** CPU 使用率不可用哨兵（与 0% 真实空载区分，由控制面判定不可用）。 */
        const val CPU_UNAVAILABLE: Double = -1.0

        /** 后端 RTT / 上报 RTT 不可用哨兵（无可达后端 / 未知）。 */
        const val RTT_UNAVAILABLE: Double = -1.0
    }
}
