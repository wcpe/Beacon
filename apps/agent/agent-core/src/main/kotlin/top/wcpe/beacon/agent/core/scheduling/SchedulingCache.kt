package top.wcpe.beacon.agent.core.scheduling

import top.wcpe.beacon.agent.api.DataSource
import top.wcpe.beacon.agent.api.DataSourceState
import top.wcpe.beacon.agent.core.client.CandidateEntry

/**
 * 候选缓存的线程安全内存态（FR-148）：持一帧 [CandidateSnapshot] + 「最近刷新是否在线成功」标志，
 * O(1) 读供 candidatesInZone / healthOf / dataSource，写由刷新循环整帧原子替换（@Volatile 指针交换）。
 *
 * @param now             当前时间提供者（毫秒），便于测试
 * @param snapshotFreshMs 快照新鲜度阈值（默认 10 分钟；超龄仍可用但标 STALE，§4.6 降级 step 4）
 */
class SchedulingCache(
    private val now: () -> Long = { System.currentTimeMillis() },
    private val snapshotFreshMs: Long = DEFAULT_SNAPSHOT_FRESH_MS,
) {
    /** 当前候选快照；null 表示尚无任何快照（未刷新且无落盘恢复）。 */
    @Volatile
    private var snapshot: CandidateSnapshot? = null

    /** 最近一次候选刷新是否在线成功：true → 数据源 CONTROL_PLANE，false → LOCAL_SNAPSHOT。 */
    @Volatile
    private var live: Boolean = false

    /** 整帧替换快照并置数据源在线态（刷新成功 live=true / 落盘恢复 live=false）。 */
    fun set(
        snapshot: CandidateSnapshot,
        live: Boolean,
    ) {
        this.snapshot = snapshot
        this.live = live
    }

    /** 刷新失败：数据源转本地快照态（不动既有快照，继续按其降级）。 */
    fun markStale() {
        live = false
    }

    /** 当前快照（供取 generatedAtMs 等）。 */
    fun current(): CandidateSnapshot? = snapshot

    /** 指定 zone 的候选条目（无该 zone 返回空表）。 */
    fun entriesInZone(zone: String): List<CandidateEntry> = snapshot?.zones?.get(zone) ?: emptyList()

    /** 指定 zone 内按 serverId 找候选条目（用于控制面 chosen 的视图补全）。 */
    fun findEntry(
        zone: String,
        serverId: String,
    ): CandidateEntry? = snapshot?.zones?.get(zone)?.firstOrNull { it.serverId == serverId }

    /** 跨全部 zone 按 serverId 找候选条目（用于 healthOf）。 */
    fun findAnywhere(serverId: String): CandidateEntry? {
        val zones = snapshot?.zones ?: return null
        for ((_, entries) in zones) {
            val hit = entries.firstOrNull { it.serverId == serverId }
            if (hit != null) {
                return hit
            }
        }
        return null
    }

    /** 当前数据来源状态与快照年龄（无快照时年龄为 MAX、非新鲜）。 */
    fun dataSource(): DataSourceState {
        val source = if (live) DataSource.CONTROL_PLANE else DataSource.LOCAL_SNAPSHOT
        val snap = snapshot ?: return DataSourceState(source, false, Long.MAX_VALUE)
        val age = now() - snap.savedAtMs
        return DataSourceState(source, age <= snapshotFreshMs, age)
    }

    private companion object {
        /** 快照新鲜度阈值（FR-148 §4.6，10 分钟；超龄仍用但标 STALE）。 */
        const val DEFAULT_SNAPSHOT_FRESH_MS = 10L * 60L * 1000L
    }
}
