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
    fun apply(manifest: FileManifest): Boolean =
        synchronized(applyLock) {
            applyInternal(manifest)
        }

    private fun applyInternal(manifest: FileManifest): Boolean {
        val applied = appliedStore.read()
        // fileTreeMd5 守卫：与已落盘那一版相同则跳过（幂等），避免无谓比对与落盘。
        if (applied != null && applied.fileTreeMd5 == manifest.fileTreeMd5) return true
        return syncToManifest(manifest, applied)
    }

    /** 比对已落盘清单与目标清单，按差分执行同步；返回是否已收敛。 */
    private fun syncToManifest(
        manifest: FileManifest,
        applied: AppliedFileManifest?,
    ): Boolean {
        val plan = FileSyncer.diff(applied?.toMap() ?: emptyMap(), manifest.entries.associate { it.path to it.md5 })
        // md5 变了但差分为空（极少见，如仅 group/zone 元数据变）：仅刷新清单记录新 md5。
        if (plan.isEmpty()) return persistManifest(manifest)
        return applyPlan(manifest, plan)
    }

    /** 执行单轮同步计划：取内容 + 落盘写入 + 删除移除项 + 持久化清单。 */
    private fun applyPlan(
        manifest: FileManifest,
        plan: FileSyncPlan,
    ): Boolean {
        // 卫语句前置：取内容或写入失败即放弃整轮（fail-static：不动既有文件）。
        if (!fetchAndWrite(plan)) return false
        deleteRemoved(plan)
        val persisted = persistManifest(manifest)
        if (persisted) {
            adapter.info(
                "文件树已同步：新增=${plan.toAdd.size}，更新=${plan.toUpdate.size}，删除=${plan.toDelete.size}，" +
                    "fileTreeMd5=${manifest.fileTreeMd5}",
            )
        }
        return persisted
    }

    /** 先取齐所有需写入的内容并落盘；任一取不到或写入失败即返回 false（fail-static：不动既有文件）。 */
    private fun fetchAndWrite(plan: FileSyncPlan): Boolean {
        val fetched = fetchContents(plan) ?: return false
        return writeFetched(fetched)
    }

    /** 取齐所有需写入的内容；任一取不到即返回 null（触发 fail-static 放弃本轮）。 */
    private fun fetchContents(plan: FileSyncPlan): LinkedHashMap<String, FileContent>? {
        val fetched = LinkedHashMap<String, FileContent>()
        for (path in plan.toFetch()) {
            if (!guardPath(path, "文件路径（绝对/穿越/反斜杠），不落盘", "路径，不落盘", adapter, protectedSegments)) continue
            val content = fetchContent(path)
            if (content == null) {
                adapter.warn("取文件内容失败（path=$path），本轮文件树同步放弃，保留既有镜像不动")
                return null
            }
            fetched[path] = content
        }
        return fetched
    }

    /** 落盘写入已取齐的文件；任一写入失败即返回 false（本轮放弃，保留既有）。 */
    private fun writeFetched(fetched: Map<String, FileContent>): Boolean {
        for ((path, content) in fetched) {
            try {
                mirrorWriter.write(path, content.content)
            } catch (e: Exception) {
                adapter.error("文件落盘失败（path=$path），本轮放弃", e)
                return false
            }
        }
        return true
    }

    /** 删除差分中的移除项；非法 / 受保护路径跳过，单条删除失败仅告警不阻断。 */
    private fun deleteRemoved(plan: FileSyncPlan) {
        for (path in plan.toDelete) {
            if (!guardPath(path, "删除路径", "删除", adapter, protectedSegments)) continue
            try {
                mirrorWriter.delete(path)
            } catch (e: Exception) {
                adapter.warn("删除本地镜像失败（path=$path），继续：${e.message}")
            }
        }
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

/**
 * 路径守卫（文件级私有，不占 [FileTreeApplier] 方法数）：非法（绝对/穿越/反斜杠）或顶段命中 agent 自身
 * dataFolder（[protectedSegments]）即拒。[unsafeDesc] / [reservedDesc] 为跳过时的中文告警片段，区分写入 / 删除语境。
 */
private fun guardPath(
    path: String,
    unsafeDesc: String,
    reservedDesc: String,
    adapter: PlatformAdapter,
    protectedSegments: Set<String>,
): Boolean {
    if (!RelativePathGuard.isSafe(path)) {
        adapter.warn("跳过非法$unsafeDesc：$path")
        return false
    }
    if (RelativePathGuard.isReservedSelfPath(path, protectedSegments)) {
        // 自我保护：受保护顶段既不写也不删（避免运维误塞 / 旧版臆测落盘后被本版清理）。
        adapter.warn("跳过 agent 自身 dataFolder $reservedDesc：$path（受保护集合：$protectedSegments）")
        return false
    }
    return true
}
