package top.wcpe.beacon.agent.core.override

import top.wcpe.beacon.agent.core.filetree.FileContent
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import java.io.File
import java.util.concurrent.ConcurrentHashMap

/**
 * 三方覆盖集同步编排（FR-15 接线，ADR-0011）：拿投递清单 → 逐集落 targetRoot → 命中白名单才派发重载命令。
 *
 * 单集处理序（委托 [OverrideApplier]，决策 2/4/5/6/7）：
 * 1. 取齐该集全部成员内容（[fetchMember]）；**任一取不到即整集放弃本轮**（fail-static：不动该集既有文件、不派发命令、不更新基准，下轮重试）；
 * 2. 经 [OverrideApplier] 备份 → Path 级安全校验 → 反馈环防护 → 原子覆盖 → 受管标记；
 * 3. 全部成员落盘成功后，命中本地白名单才经 [ReloadCommandExecutor] 派发重载命令（白名单空则不派发并告警）。
 *
 * **回滚只还原不重放命令**：本类不主动回滚（决策 5「崩溃向目标态收敛重做、不自动回滚」）；
 * 备份能力供调用方在外部需要时回滚，回滚绝不触碰命令执行器。
 *
 * fail-static（决策 5 / 架构不变量）：控制面不可用 / 取内容失败时不臆测、不删既有、不派发命令。
 *
 * 幂等收敛：记每集已应用的 overrideMd5 基准；同 md5 跳过（避免无谓重盖与重复 reload）。
 * 仅记内存，重启后基准重建——重启后首轮按目标态重做一次（幂等），符合「向目标态收敛」。
 *
 * 全程在异步线程由调用方（lifecycle）驱动，本类不阻塞主线程。
 *
 * @param pluginsBaseFolder 插件 plugins 基目录（覆盖集 targetRoot 形如 plugins/<plugin>，落此目录的父级即服务器根）
 * @param backupRoot        覆盖前备份区根目录（落 agent 数据目录下，与镜像目标分离）
 * @param whitelist         本地命令白名单（默认空 = 命令派发能力关闭，控制面不下发）
 * @param adapter           平台适配（异步调度、命令派发、日志）
 * @param fetchMember       取某 (setName, path) 成员整文件内容；返回 null 表示取不到（触发该集 fail-static 放弃本轮）
 */
class OverrideSyncApplier(
    private val pluginsBaseFolder: File,
    private val backupRoot: File,
    private val whitelist: CommandWhitelist,
    private val adapter: PlatformAdapter,
    private val fetchMember: (setName: String, path: String) -> FileContent?,
) {

    // 共享备份器与受管标记（按集隔离 setId / path，跨集不冲突）。
    private val backupManager = BackupManager(backupRoot)
    private val tracker = ManagedFileTracker()
    private val reloadExecutor = ReloadCommandExecutor(whitelist, adapter)

    // 目标根 agent 侧最终校验（防控制面被攻破下发恶意 targetRoot 逃逸 plugins，决策 4）。
    private val targetRootSecurity = TargetRootSecurity(pluginsBaseFolder)

    // 每个 setName 已成功应用的 overrideMd5 基准（幂等守卫）。
    private val appliedMd5 = ConcurrentHashMap<String, String>()

    // 最近一次「整轮全部集都已收敛」时的整体 overrideMd5（长轮询比对基准）；尚未全收敛为 null（强制重拉）。
    @Volatile
    private var convergedOverrideMd5: String? = null

    // 服务器根目录（plugins 基目录的父级）；targetRoot 形如 plugins/<plugin>，相对它解析。
    private val serverRoot: File = pluginsBaseFolder.parentFile ?: pluginsBaseFolder

    /**
     * 当前已整轮收敛那一版的 overrideMd5；尚未全收敛为 null（首启 / 上轮有集失败 → 长轮询 md5 传空强制重拉）。
     */
    fun currentOverrideMd5(): String? = convergedOverrideMd5

    /**
     * 应用一份投递清单。
     *
     * @return true 表示本轮全部集都已收敛（无变更或已落盘）；false 表示至少一集因取内容失败放弃（保留既有，下次重试）。
     */
    fun apply(manifest: OverrideManifest): Boolean {
        var allConverged = true
        val seen = HashSet<String>()
        for (set in manifest.sets) {
            seen.add(set.name)
            if (!applyOne(manifest.overrideMd5, set)) {
                allConverged = false
            }
        }
        // 清理已不再适用的集基准（集被下线 / 移出覆盖链）：仅忘记基准，**不动其已落盘文件**（fail-static 不臆测删盘）。
        appliedMd5.keys.retainAll { it in seen }
        // 仅整轮全收敛才记整体 md5 作长轮询基准；有集失败保持旧基准（下轮带空 md5 或旧 md5 重拉重做）。
        if (allConverged) {
            convergedOverrideMd5 = manifest.overrideMd5
        }
        return allConverged
    }

    /** 处理单个覆盖集：取齐成员 → 落盘 → 命中白名单才派发命令。返回是否已收敛。 */
    private fun applyOne(overrideMd5: String, set: OverrideSetEntry): Boolean {
        // 幂等守卫：该集目标态（以整体 overrideMd5 为基准）未变则跳过。
        if (appliedMd5[set.name] == overrideMd5) {
            return true
        }

        // agent 最终权威：拒绝逃逸 plugins 的恶意 targetRoot（控制面被攻破兜底，决策 4）。整集拒绝、不落盘、不派发命令。
        if (!targetRootSecurity.isSafe(set.targetRoot)) {
            adapter.warn("拒绝非法覆盖集目标根（逃逸 plugins / 盘符 / 穿越 / 保留名），整集跳过：set=${set.name}，targetRoot=${set.targetRoot}")
            return false
        }

        val targetRoot = File(serverRoot, set.targetRoot)
        // 先取齐所有成员内容；任一取不到即整集放弃本轮（fail-static：不动既有、不派发命令、不更新基准）。
        val files = ArrayList<OverrideFile>(set.members.size)
        for (path in set.members) {
            val content = fetchMember(set.name, path)
            if (content == null) {
                adapter.warn("取覆盖集成员内容失败（set=${set.name}，path=$path），本轮放弃该集，保留既有不动，下轮重试")
                return false
            }
            files.add(OverrideFile(path = content.path, content = content.content, md5 = content.md5))
        }

        val applier = OverrideApplier(
            targetRoot = targetRoot,
            backupManager = backupManager,
            tracker = tracker,
            pathSecurity = OverridePathSecurity(targetRoot),
            reloadExecutor = reloadExecutor,
            adapter = adapter,
        )
        // 落盘 + （全量成功且非空命令命中白名单才）派发重载命令。
        val allWritten = applier.apply(set.name, files, set.reloadCommand)
        if (allWritten) {
            // 仅全量落盘成功才更新基准（幂等收敛）；部分失败保留旧基准，下轮重做向目标态收敛。
            appliedMd5[set.name] = overrideMd5
            adapter.info("覆盖集已应用：set=${set.name}，targetRoot=${set.targetRoot}，成员=${set.members.size}")
        }
        return allWritten
    }
}
