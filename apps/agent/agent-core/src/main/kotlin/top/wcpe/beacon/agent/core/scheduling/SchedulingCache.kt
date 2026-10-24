package top.wcpe.beacon.agent.core.scheduling

import top.wcpe.beacon.agent.api.DataSource
import top.wcpe.beacon.agent.api.DataSourceState
import top.wcpe.beacon.agent.core.client.CandidateEntry

/**
 * 池段选择器（FR-281）：入口大厅与共享游戏池在缓存里是两段独立的候选，语义完全对称，
 * 故读写入口按段参数化，而不是各写一套同构方法。
 */
enum class PoolSection {
    /** 入口大厅池：BC 首次落脚用，成员不属于任何小区。 */
    LOBBY,

    /** 共享游戏池：跨区共享的游戏服池，成员同样不属于任何小区，但与入口大厅成员互斥。 */
    SHARED,
}

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

    /**
     * 池段候选条目：**非 null（含空表）= 本帧有该池模型**，空表即"池里没有可调度成员"这一稳定事实；
     * **null = 本帧没有该池模型**（尚无任何快照，或本帧来源是还没下发该段的旧控制面）——那是"看不到"，
     * 调用方必须按不可用处置，**不得**读成"池里没有机器"。
     *
     * 入口大厅与共享游戏池共用本方法：两段的 nullable 语义完全一致，分两个方法只会多一份同样的判定。
     */
    fun poolEntries(pool: PoolSection): List<CandidateEntry>? =
        when (pool) {
            PoolSection.LOBBY -> snapshot?.lobby?.candidates
            PoolSection.SHARED -> snapshot?.shared?.candidates
        }

    /** 指定池段内按 serverId 找候选条目（用于控制面 chosen 的视图补全）。 */
    fun findInPool(
        pool: PoolSection,
        serverId: String,
    ): CandidateEntry? = poolEntries(pool)?.firstOrNull { it.serverId == serverId }

    /**
     * 跨全部 zone 与两个池段（入口大厅 / 共享游戏池）按 serverId 找候选条目（用于 healthOf）。
     *
     * <p>必须覆盖三处：两池成员**都不属于任何小区**，只按小区找会让池成员的 healthOf 恒为 null。
     * 顺序为小区段 → 大厅段 → 共享池段：归属四选一保证同一台服只会出现在其中一处，故顺序不影响结果，
     * 只决定命中时的查找先后。</p>
     */
    fun findAnywhere(serverId: String): CandidateEntry? {
        val zones = snapshot?.zones
        if (zones != null) {
            for ((_, entries) in zones) {
                val hit = entries.firstOrNull { it.serverId == serverId }
                if (hit != null) {
                    return hit
                }
            }
        }
        return findInPool(PoolSection.LOBBY, serverId) ?: findInPool(PoolSection.SHARED, serverId)
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
