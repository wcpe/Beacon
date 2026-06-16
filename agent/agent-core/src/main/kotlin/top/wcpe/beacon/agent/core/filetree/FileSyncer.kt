package top.wcpe.beacon.agent.core.filetree

/**
 * 文件树差分纯逻辑：把目标清单（manifest，path→md5）与本地已落盘清单（applied-manifest）比对，
 * 算出需新增 / 更新 / 删除的相对路径集合。无副作用，便于穷举单测。
 *
 * 语义为「整文件覆盖」（不深合并，见 ADR-0010）：
 * - toAdd：目标有、本地无的 path。
 * - toUpdate：两边都有但 md5 不同的 path。
 * - toDelete：本地有、目标无的 path（高层删 path 或整体下线）。
 *
 * fail-static 由上层把控（无目标态时根本不调用本差分，绝不臆测删文件）；本类只在确有目标清单时计算。
 */
object FileSyncer {

    /**
     * 计算从 [applied]（本地已落盘 path→md5）到 [target]（目标 path→md5）的差分。
     */
    fun diff(applied: Map<String, String>, target: Map<String, String>): FileSyncPlan {
        val toAdd = LinkedHashSet<String>()
        val toUpdate = LinkedHashSet<String>()
        val toDelete = LinkedHashSet<String>()

        // 目标侧：本地缺 → 新增；md5 不同 → 更新；相同 → 跳过。
        for ((path, md5) in target) {
            val prev = applied[path]
            when {
                prev == null -> toAdd.add(path)
                prev != md5 -> toUpdate.add(path)
            }
        }
        // 本地侧：目标已无 → 删除。
        for (path in applied.keys) {
            if (!target.containsKey(path)) {
                toDelete.add(path)
            }
        }
        return FileSyncPlan(toAdd = toAdd, toUpdate = toUpdate, toDelete = toDelete)
    }
}

/**
 * 一次文件树同步的差分计划。
 *
 * @param toAdd    需新增落盘的相对路径
 * @param toUpdate 需覆盖更新的相对路径
 * @param toDelete 需删除本地镜像的相对路径
 */
data class FileSyncPlan(
    val toAdd: Set<String>,
    val toUpdate: Set<String>,
    val toDelete: Set<String>,
) {
    /** 是否无任何变更（增删改皆空）。 */
    fun isEmpty(): Boolean = toAdd.isEmpty() && toUpdate.isEmpty() && toDelete.isEmpty()

    /** 需向控制面取内容的路径集合（新增 + 更新）。 */
    fun toFetch(): Set<String> = LinkedHashSet<String>().apply {
        addAll(toAdd)
        addAll(toUpdate)
    }
}
