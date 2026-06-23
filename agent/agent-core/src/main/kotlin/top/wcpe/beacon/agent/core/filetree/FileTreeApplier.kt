package top.wcpe.beacon.agent.core.filetree

import top.wcpe.beacon.agent.core.platform.PlatformAdapter

/**
 * 文件树同步编排：拿目标清单与本地已落盘清单比对 → 仅取变更文件 → 镜像落盘 → 持久化清单。
 *
 * 落盘序「先文件后清单」：先把新增/更新文件原子写盘并 fsync、删除移除项，全部成功后才写 applied-manifest。
 * 崩溃恢复时清单只反映已落盘的部分，下一轮据差异补齐。
 *
 * fail-static（比配置更保守，见 ADR-0010 决策5）：
 * - 任一变更文件取内容失败（控制面不可用 / 该 path 已不在有效树）→ **整轮放弃**，不写清单、不删任何文件，
 *   保留既有镜像不动，下一轮重试。绝不臆测删文件。
 * - 路径非法的条目跳过（告警），不阻断其余安全条目落盘。
 *
 * 自我保护（与 FR-41 env 注入身份相辅相成）：
 * - 顶段命中 [protectedSegments]（壳层注入的 agent 自身 plugin 名集合，如 `BeaconAgent` / `BeaconAgentProxy`）
 *   的 path 视为"agent 自管"，applier 既不取内容也不落盘也不删除，并打 WARN 便于运维核对。
 *   防止运维误把 `BeaconAgent/config.yml` 之类经 FR-14 文件树或 FR-38 导入塞进有效树后，agent 覆写自身。
 *   空集合（默认）= 未启用保护，回到旧语义，保留兼容。
 *
 * @param mirrorWriter      原子落盘器（目标根内）
 * @param appliedStore      本地已落盘清单读写
 * @param adapter           平台适配（仅日志）
 * @param fetchContent      取单个 path 整文件内容；返回 null 表示取不到（触发 fail-static 放弃本轮）
 * @param protectedSegments agent 自身 dataFolder 顶段名（如 `BeaconAgent`）；命中顶段的 path 一律跳过
 */
class FileTreeApplier(
    private val mirrorWriter: FileMirrorWriter,
    private val appliedStore: AppliedFileManifestStore,
    private val adapter: PlatformAdapter,
    private val fetchContent: (path: String) -> FileContent?,
    private val protectedSegments: Set<String> = emptySet(),
) {

    /**
     * 当前本地已落盘那一版的 fileTreeMd5；尚无落盘清单时为 null（首启长轮询 md5 传空）。
     */
    fun currentFileTreeMd5(): String? = appliedStore.read()?.fileTreeMd5

    /** 串行化 apply：长轮询循环 / SSE file-changed / 运维 resync 可并发触发，加锁避免抢同一临时文件与清单读改写竞争。 */
    private val applyLock = Any()

    /**
     * 应用一份目标清单。
     *
     * 并发安全：多路触发经 [applyLock] 串行执行；落盘原子写由 [AtomicFileWriter] 保证（唯一 tmp + 重命名回退/重试）。
     *
     * @return true 表示已收敛（无变更或已落盘并更新清单）；false 表示因取内容失败 / 清单写入失败放弃本轮（保留既有，下次重试）。
     */
    fun apply(manifest: FileManifest): Boolean = synchronized(applyLock) {
        applyInternal(manifest)
    }

    private fun applyInternal(manifest: FileManifest): Boolean {
        val applied = appliedStore.read()
        // fileTreeMd5 守卫：与已落盘那一版相同则跳过（幂等），避免无谓比对与落盘。
        if (applied != null && applied.fileTreeMd5 == manifest.fileTreeMd5) {
            return true
        }

        val appliedMap = applied?.toMap() ?: emptyMap()
        val targetMap = manifest.entries.associate { it.path to it.md5 }
        val plan = FileSyncer.diff(appliedMap, targetMap)
        if (plan.isEmpty()) {
            // md5 变了但差分为空（极少见，如仅 group/zone 元数据变）：仅刷新清单记录新 md5。
            return persistManifest(manifest)
        }

        // 先取齐所有需写入的内容；任一取不到即放弃整轮（fail-static：不动既有文件）。
        val fetched = LinkedHashMap<String, FileContent>()
        for (path in plan.toFetch()) {
            if (!RelativePathGuard.isSafe(path)) {
                adapter.warn("跳过非法文件路径（绝对/穿越/反斜杠），不落盘：$path")
                continue
            }
            if (RelativePathGuard.isReservedSelfPath(path, protectedSegments)) {
                // 自我保护：path 顶段命中 agent 自身 dataFolder，跳过——不取、不写。
                adapter.warn("跳过 agent 自身 dataFolder 路径，不落盘：$path（受保护集合：$protectedSegments）")
                continue
            }
            val content = fetchContent(path)
            if (content == null) {
                adapter.warn("取文件内容失败（path=$path），本轮文件树同步放弃，保留既有镜像不动")
                return false
            }
            fetched[path] = content
        }

        // 落盘：先写新增/更新文件（已 fsync），再删除移除项。
        var writeFailed = false
        for ((path, content) in fetched) {
            try {
                mirrorWriter.write(path, content.content)
            } catch (e: Exception) {
                adapter.error("文件落盘失败（path=$path），本轮放弃", e)
                writeFailed = true
                break
            }
        }
        if (writeFailed) {
            return false // 落盘失败：不更新清单，保留既有，下次重试。
        }
        for (path in plan.toDelete) {
            if (!RelativePathGuard.isSafe(path)) {
                adapter.warn("跳过非法删除路径：$path")
                continue
            }
            if (RelativePathGuard.isReservedSelfPath(path, protectedSegments)) {
                // 自我保护：受保护顶段永远不删（避免曾被旧版臆测落盘后又被本版主动清理）。
                adapter.warn("跳过 agent 自身 dataFolder 删除：$path（受保护集合：$protectedSegments）")
                continue
            }
            try {
                mirrorWriter.delete(path)
            } catch (e: Exception) {
                adapter.warn("删除本地镜像失败（path=$path），继续：${e.message}")
            }
        }

        // 全部落盘成功后才写清单（先文件后清单）。清单写入失败 fail-static：保留既有、下次重试，不抛到调度器。
        if (!persistManifest(manifest)) return false
        adapter.info(
            "文件树已同步：新增=${plan.toAdd.size}，更新=${plan.toUpdate.size}，删除=${plan.toDelete.size}，" +
                "fileTreeMd5=${manifest.fileTreeMd5}",
        )
        return true
    }

    /** 写已落盘清单：失败不抛（fail-static），返回 false 让本轮放弃、下次重试。 */
    private fun persistManifest(manifest: FileManifest): Boolean {
        return try {
            appliedStore.write(manifest.fileTreeMd5, manifest.entries)
            true
        } catch (e: Exception) {
            adapter.error("已落盘清单写入失败，本轮放弃（保留既有镜像不动，下次重试）", e)
            false
        }
    }
}
