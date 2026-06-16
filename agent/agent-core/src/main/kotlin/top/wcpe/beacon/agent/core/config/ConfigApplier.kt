package top.wcpe.beacon.agent.core.config

import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.snapshot.SnapshotStore

/**
 * 有效配置 apply 编排：md5 守卫 → 更新内存 store → 写快照 → 广播变更。
 *
 * agent 不重载业务配置，只暴露有效文本 + 发通知；具体热更由业务插件在回调里实现。
 *
 * @param store         内存有效配置存储
 * @param snapshotStore 本地快照（snapshotEnabled=false 时传 null）
 * @param adapter       平台适配（日志 / 事件派发）
 */
class ConfigApplier(
    private val store: EffectiveConfigStore,
    private val snapshotStore: SnapshotStore?,
    private val adapter: PlatformAdapter,
) {

    /**
     * 应用一份新有效配置。
     *
     * @return true 表示确有变更并已广播；false 表示 md5 相同跳过（幂等）。
     */
    fun apply(result: EffectiveResult): Boolean {
        // md5 守卫：相同则跳过，避免无意义的写快照与广播。
        if (result.md5 == store.currentMd5()) {
            return false
        }

        // 计算变更的 dataId 集合（新增 / 内容变化 / 删除）。
        val changed = computeChangedDataIds(result)

        // 更新内存（写锁）。
        store.replace(result)

        // 写快照（失败不影响有效配置生效，仅告警）。
        if (snapshotStore != null) {
            try {
                snapshotStore.write(result)
            } catch (e: Exception) {
                adapter.error("写本地快照失败，有效配置仍已生效", e)
            }
        }

        // 广播变更给业务插件。
        adapter.publishConfigChanged(changed, result.md5)
        adapter.info("有效配置已更新，md5=${result.md5}，变更项=${changed.size}")
        return true
    }

    /** 比较旧 store 与新结果，得出变更的 dataId（新增、md5 变化、删除）。 */
    private fun computeChangedDataIds(result: EffectiveResult): Set<String> {
        val old = store.snapshotItems().associateBy { it.dataId }
        val new = result.items.associateBy { it.dataId }
        val changed = LinkedHashSet<String>()
        // 新增或内容变化。
        for ((dataId, item) in new) {
            val prev = old[dataId]
            if (prev == null || prev.md5 != item.md5) {
                changed.add(dataId)
            }
        }
        // 删除。
        for (dataId in old.keys) {
            if (!new.containsKey(dataId)) {
                changed.add(dataId)
            }
        }
        return changed
    }
}
