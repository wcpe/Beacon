package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.AssetManifestMeta
import top.wcpe.beacon.agent.core.client.AssetManifestOutcome
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.command.AssetEntry
import top.wcpe.beacon.agent.core.command.AssetIndexLimits
import top.wcpe.beacon.agent.core.command.AssetIndexReader
import top.wcpe.beacon.agent.core.command.AssetManifestDigest
import top.wcpe.beacon.agent.core.command.AssetScanResult
import top.wcpe.beacon.agent.core.filetree.AssetManifestStore
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import java.io.File
import java.util.UUID
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicReference
import kotlin.random.Random

/**
 * 资产扫描目标（FR-163）：服务器工作目录 + agent 自身数据目录。
 *
 * @param serverRoot   服务器工作目录提供者（agent 壳层：pluginsBaseFolder 的父目录）
 * @param selfDataDir  agent 自身数据目录（如 plugins/BeaconAgent）：整棵排除出扫描，防自写缓存 / 快照自我指涉致清单不收敛
 */
data class AssetScanScope(
    val serverRoot: () -> File,
    val selfDataDir: File,
)

/**
 * 文件资产索引周期扫描 + 增量上报协调器（FR-163，见 ADR asset-manifest-sync-protocol）。
 * 从 [AgentLifecycle] 拆出，避免其膨胀成上帝类（AGENTS §10.1）。
 *
 * 单条「代」循环，随注册成功 [start]（幂等，重注册不重启）、停机 [stop]（递增代使旧 tick 失效）：
 * 每周期在 async 线程扫描本机资产（[AssetIndexReader]，纯 java.nio、绝不上 MC 主线程），算清单摘要与本地已确认摘要比对——
 * - **零变更周期不发请求**；仅每满 [KEEPALIVE_EVERY_CYCLES] 个周期发一次空 delta 保活刷新概要 scannedAt（ADR 决策 5）。
 * - **有变更**：本地有已确认清单 → 增量 delta（upserts=条目变化项、deleted=消失项、baseDigest=已确认摘要）；
 *   本地无已确认清单（首次 / 缓存缺失）→ 全量分片（>2000 条按 uploadId+seq 分片、末批 eof）。
 * - **基线失配 409 或摘要不一致** → 本周期立即改发全量强制归零（ADR 决策 2/3）。
 * - **控制面不可达** → fail-static：保留本地状态、静默等下周期，不重试风暴（[noteHealth] warn-once）。
 *
 * 上报按 (返回摘要 == 本地摘要) 才落库本地清单缓存（[AssetManifestStore]），供重启续增量。
 * 同服串行（规格 §4.3）：周期 tick 与 asset-rescan 手动重扫经 [reportLock] 串行化，杜绝并发交叉写。
 */
class AssetScanCoordinator(
    private val adapter: PlatformAdapter,
    private val apiClient: BeaconApiClient,
    private val identity: AgentIdentity,
    private val store: AssetManifestStore,
    private val scope: AssetScanScope,
    intervalMs: Long,
) {
    /** 是否在运行（start 置 true、stop 置 false，循环据此退出）。 */
    private val active = AtomicBoolean(false)

    /** 扫描循环的「代」标识：start 递增使旧循环退出。 */
    private val scanGen = AtomicReference(0)

    /** 周期扫描间隔（毫秒）。 */
    @Volatile
    private var intervalMs: Long = intervalMs

    /** 连续零变更周期计数：每满 [KEEPALIVE_EVERY_CYCLES] 个发一次空 delta 保活（仅周期 tick 内访问）。 */
    @Volatile
    private var skippedCycles: Int = 0

    /** 上报健康标志：仅在转入失败 / 恢复各记一次日志，避免断连期每周期刷屏。 */
    @Volatile
    private var reportHealthy: Boolean = true

    /** 同服串行锁（规格 §4.3）：周期 tick 与手动重扫并发时串行化「扫描+上报+落盘」，避免交叉写。 */
    private val reportLock = Any()

    /** 启动周期扫描循环（随注册成功调用）。**幂等**：已激活时重复 start 直接返回，不重启现有循环。 */
    fun start() {
        if (!active.compareAndSet(false, true)) return
        val gen = scanGen.get() + 1
        scanGen.set(gen)
        // 起始加 0~10% 周期随机抖动（防 1000 台同时上报踩踏，规格 §4.2）。
        val initialDelay = (intervalMs * Random.nextDouble(0.0, JITTER_RATIO)).toLong()
        adapter.runAsyncDelayed(initialDelay) { scanTick(gen) }
    }

    /** 停止扫描循环（随 shutdown 调用）。 */
    fun stop() {
        active.set(false)
    }

    /**
     * asset-rescan 命令回调（规格 §4.2）：立即扫描本机资产并**全量**上报（重扫即重同步）。
     * [force]=true 忽略本地 mtime 缓存全部重哈希。**须在 async 线程调用**（读盘 + 哈希 + HTTP 阻塞 IO）。
     *
     * @return true=已派发（即便控制面暂不可达，扫描已执行、上报按 fail-static 等下周期）；false=内部异常
     */
    fun forceScanNow(force: Boolean): Boolean {
        return try {
            synchronized(reportLock) {
                val previous = store.read()?.entriesMap() ?: emptyMap()
                reportFull(scanOnce(previous, force))
            }
            true
        } catch (e: Exception) {
            adapter.error("文件资产手动重扫失败：${e.message}", e)
            false
        }
    }

    private fun scanTick(gen: Int) {
        if (!active.get() || gen != scanGen.get()) return
        try {
            synchronized(reportLock) { runScanCycle() }
        } catch (e: Exception) {
            // fail-static：扫描 / 上报任何异常绝不让 async 线程崩、绝不影响 agent 其它职责。
            adapter.warn("文件资产扫描周期异常（已忽略，等下周期）：${e.message}")
        }
        // 续杯下一周期（stop 后 active=false 即不再续，旧代 tick 亦被 gen 守卫挡下）。
        if (active.get()) adapter.runAsyncDelayed(intervalMs) { scanTick(gen) }
    }

    /** 一次周期扫描：算摘要与已确认比对——零变更跳过（每 6 周期空 delta 保活），有变更按 delta / full 上报。 */
    private fun runScanCycle() {
        val stored = store.read()
        val previous = stored?.entriesMap() ?: emptyMap()
        val cycle = scanOnce(previous, force = false)
        val confirmedDigest = stored?.confirmedDigest ?: ""

        // 零变更周期：不发请求；每满 KEEPALIVE_EVERY_CYCLES 发一次空 delta 保活刷新概要 scannedAt（ADR 决策 5）。
        if (stored != null && cycle.localDigest == confirmedDigest) {
            skippedCycles += 1
            if (skippedCycles >= KEEPALIVE_EVERY_CYCLES) {
                skippedCycles = 0
                reportDelta(confirmedDigest, emptyList(), emptyList(), cycle)
            }
            return
        }

        skippedCycles = 0
        // 本地有已确认清单 → 增量；无（首次 / 缓存缺失 / 损坏）→ 全量兜底。
        if (stored != null && stored.confirmedDigest.isNotEmpty()) {
            val upserts = cycle.result.entries.filter { previous[it.path] != it }
            val currentPaths = cycle.result.entries.mapTo(HashSet()) { it.path }
            val deleted = previous.keys.filter { it !in currentPaths }
            reportDelta(stored.confirmedDigest, upserts, deleted, cycle)
        } else {
            reportFull(cycle)
        }
    }

    /** 扫描一次并算本地摘要（不上报）。 */
    private fun scanOnce(
        previous: Map<String, AssetEntry>,
        force: Boolean,
    ): ScanCycle {
        val start = System.currentTimeMillis()
        val result = AssetIndexReader.scan(scope.serverRoot(), previous, force, scope.selfDataDir)
        val durationMs = System.currentTimeMillis() - start
        return ScanCycle(result, AssetManifestDigest.computeManifestDigest(result.entries), start, durationMs)
    }

    /** 增量上报：受理且摘要一致 → 落库；409 或摘要不一致 → 本周期改发全量强制归零；拒绝 → 告警；失败 → fail-static。 */
    private fun reportDelta(
        baseDigest: String,
        upserts: List<AssetEntry>,
        deleted: List<String>,
        cycle: ScanCycle,
    ) {
        val meta = AssetManifestMeta("delta", cycle.scannedAtMs, cycle.scanDurationMs, cycle.result.truncated, upserts)
        when (val outcome = apiClient.reportAssetManifest(identity, meta, baseDigest = baseDigest, deleted = deleted)) {
            is AssetManifestOutcome.Accepted -> {
                noteHealth(ok = true, reason = "")
                if (outcome.digest == cycle.localDigest) {
                    persistIfConverged(outcome.digest, cycle)
                } else {
                    // 摘要与控制面重算不一致（罕见漂移）：本周期立即改发全量强制归零（ADR 决策 2/3）。
                    adapter.info("文件资产 delta 后摘要与控制面不一致，改发全量重同步")
                    reportFull(cycle)
                }
            }

            AssetManifestOutcome.OutOfSync -> {
                noteHealth(ok = true, reason = "")
                adapter.info("文件资产清单基线失配（409），改发全量重同步")
                reportFull(cycle)
            }

            is AssetManifestOutcome.Rejected -> adapter.warn("文件资产清单增量上报被拒：${outcome.code}")
            is AssetManifestOutcome.Failed -> noteHealth(ok = false, reason = outcome.reason)
        }
    }

    /** 全量上报：>2000 条按 uploadId 分片（seq 递增、末批 eof）；末批摘要与本地一致才落库。空清单也发一个 eof 空分片。 */
    private fun reportFull(cycle: ScanCycle) {
        val uploadId = UUID.randomUUID().toString()
        val shards = cycle.result.entries.chunked(AssetIndexLimits.MAX_MANIFEST_UPLOAD).ifEmpty { listOf(emptyList<AssetEntry>()) }
        for ((index, shard) in shards.withIndex()) {
            val eof = index == shards.size - 1
            val meta = AssetManifestMeta("full", cycle.scannedAtMs, cycle.scanDurationMs, cycle.result.truncated, shard)
            val outcome = apiClient.reportAssetManifest(identity, meta, uploadId = uploadId, seq = index, eof = eof)
            if (outcome is AssetManifestOutcome.Accepted) {
                noteHealth(ok = true, reason = "")
                if (eof) persistIfConverged(outcome.digest, cycle)
            } else {
                // 任一分片未受理即中断，不落库（下周期重来）：OutOfSync 记提示，Rejected 告警，Failed 走 fail-static。
                when (outcome) {
                    AssetManifestOutcome.OutOfSync -> adapter.info("文件资产全量分片被拒（暂存丢失 / 过期 / seq 跳号），下周期重来")
                    is AssetManifestOutcome.Rejected -> adapter.warn("文件资产全量上报被拒：${outcome.code}")
                    is AssetManifestOutcome.Failed -> noteHealth(ok = false, reason = outcome.reason)
                    is AssetManifestOutcome.Accepted -> Unit
                }
                return
            }
        }
    }

    /** 返回摘要与本地一致才落库（供重启续增量），否则记不一致、下周期重来；写盘失败不影响上报。 */
    private fun persistIfConverged(
        returnedDigest: String,
        cycle: ScanCycle,
    ) {
        if (returnedDigest != cycle.localDigest) {
            adapter.info("文件资产上报后摘要与控制面不一致（本地 ${cycle.localDigest} ≠ 返回 $returnedDigest），下周期重同步")
            return
        }
        try {
            store.write(cycle.localDigest, cycle.result.entries)
        } catch (e: Exception) {
            adapter.warn("文件资产清单本地缓存写入失败（不影响上报，下周期重试）：${e.message}")
        }
    }

    /** 上报健康转移日志（warn-once）：ok 恢复 / 不可达各记一次，避免刷屏。fail-static：不可达保留本地状态、等下周期。 */
    private fun noteHealth(
        ok: Boolean,
        reason: String,
    ) {
        if (ok && !reportHealthy) {
            reportHealthy = true
            adapter.info("文件资产清单上报已恢复")
        } else if (!ok && reportHealthy) {
            reportHealthy = false
            adapter.info("文件资产清单上报暂不可达（$reason），保留本地状态、等下周期重试")
        }
    }

    /** 一次扫描的上下文（清单结果 + 本地摘要 + 扫描时刻 / 耗时），收敛上报方法参数。 */
    private data class ScanCycle(
        val result: AssetScanResult,
        val localDigest: String,
        val scannedAtMs: Long,
        val scanDurationMs: Long,
    )

    private companion object {
        /** 保活周期数（ADR 决策 5）：每满这么多个零变更周期发一次空 delta 刷新概要 scannedAt。 */
        const val KEEPALIVE_EVERY_CYCLES = 6

        /** 起始抖动比例上限（规格 §4.2）：首次扫描延迟 = 周期 × [0, 该值)。 */
        const val JITTER_RATIO = 0.1
    }
}
