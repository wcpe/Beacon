package top.wcpe.beacon.agent.core.scheduling

import top.wcpe.beacon.agent.api.CandidateView
import top.wcpe.beacon.agent.api.HealthLevel
import top.wcpe.beacon.agent.api.HealthView
import top.wcpe.beacon.agent.core.client.CandidateEntry
import top.wcpe.beacon.agent.core.client.SchedCandidates
import top.wcpe.beacon.agent.core.client.SelfHealth
import java.util.UUID

/**
 * 本机候选缓存的一帧内存快照（FR-148 降级路径）。
 *
 * @param generatedAtMs 控制面生成时刻（快照内容时间）
 * @param savedAtMs     本机接收 / 落盘时刻（新鲜度基准，dataSource 年龄以此计）
 * @param zones         zone 名 → 候选列表（控制面已按分降序）
 */
data class CandidateSnapshot(
    val generatedAtMs: Long,
    val savedAtMs: Long,
    val zones: Map<String, List<CandidateEntry>>,
)

/** 带接收时刻的自身健康视图（selfHealth 的 sampledAtMs 取此时刻）。 */
data class TimedSelfHealth(
    val self: SelfHealth,
    val atMs: Long,
)

/**
 * 调度候选刷新循环的生命周期端口（FR-148）：由 [AgentLifecycle] 在注册成功时 [start]、停机时 [stop]、
 * 启动时 [restoreSnapshot] 从落盘快照恢复。与 BeaconScheduling 门面分离，使门面只读、循环只管刷新（SRP）。
 */
interface SchedulingRuntime {
    /** 启动 10s 候选刷新循环（幂等，重注册不重启）。 */
    fun start()

    /** 停止刷新循环。 */
    fun stop()

    /** 启动期从落盘快照恢复候选缓存（供注册前 fail-static 降级；无快照时 no-op）。 */
    fun restoreSnapshot()
}

/** 小写线上 level 值映射到 API 强类型枚举（未知一律按 UNHEALTHY 保守处理）。 */
internal fun mapLevel(wire: String): HealthLevel =
    when (wire) {
        "healthy" -> HealthLevel.HEALTHY
        "degraded" -> HealthLevel.DEGRADED
        else -> HealthLevel.UNHEALTHY
    }

/** 候选 wire 条目 → API 候选视图（zone 由调用侧提供，wire 不重复携带）。 */
internal fun CandidateEntry.toCandidateView(zone: String): CandidateView =
    CandidateView(serverId, zone, score, mapLevel(level), onlineCount, maxOnline)

/** 候选 wire 条目 → API 健康视图（候选快照不含 reasons，故为空表；schedulable 取快照标记）。 */
internal fun CandidateEntry.toHealthView(sampledAtMs: Long): HealthView =
    HealthView(serverId, score, mapLevel(level), schedulable, emptyList(), sampledAtMs)

/** 自身健康 wire → API 健康视图。 */
internal fun SelfHealth.toHealthView(
    serverId: String,
    sampledAtMs: Long,
): HealthView = HealthView(serverId, score, mapLevel(level), schedulable, reasons, sampledAtMs)

/** candidates 响应 → 内存候选快照（zone 名保序、savedAt 取接收时刻）。 */
internal fun SchedCandidates.toSnapshot(nowMs: Long): CandidateSnapshot {
    val map = LinkedHashMap<String, List<CandidateEntry>>()
    for (zone in zones) {
        map[zone.zone] = zone.candidates
    }
    return CandidateSnapshot(generatedAtMs = generatedAtMs, savedAtMs = nowMs, zones = map)
}

/** 生成本地决策 traceId（UUID，与控制面 traceId 同形，供降级期决策 / 补报幂等键）。 */
internal fun newLocalTraceId(): String = UUID.randomUUID().toString()
