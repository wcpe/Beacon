package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.FileManifestPollResult
import top.wcpe.beacon.agent.core.client.PollResult
import top.wcpe.beacon.agent.core.client.pollEffective
import top.wcpe.beacon.agent.core.client.pollFileManifest

/**
 * 立即重拉有效配置（运维 reload）：以 md5=null 强制一次拉取并 apply，旁路长轮询 304，不等超时。
 *
 * 复用 ConfigApplier 的 md5 幂等守卫：内容未变则只触发一次无害读取、不重复广播。
 * 独立一发，不接管长轮询主循环、不改其代标识。
 */
fun AgentLifecycle.forcePollNow() {
    if (!running.get()) return
    adapter.info("收到 reload：强制立刻重拉有效配置并 apply")
    adapter.runAsync {
        when (val result = apiClient.pollEffective(identity, currentMd5 = null, timeoutMs = settings.requestTimeoutMs)) {
            is PollResult.Changed -> {
                applier.apply(result.effective)
                reportApplied(result.effective.md5)
            }

            is PollResult.NotModified -> adapter.info("reload 完成：有效配置无变更")
            is PollResult.NotRegistered -> {
                adapter.warn("reload 时返回未注册，触发重新接入")
                triggerReregister()
            }

            is PollResult.Failed -> adapter.warn("reload 强制重拉失败（${result.reason}），保持当前有效配置")
        }
    }
}

/**
 * 立即强制重同步（FR-91）：重拉控制面权威的有效配置/文件树/覆盖集并 apply。
 *
 * 复用现有三条「以本地 md5 拉一次 → 幂等 apply」路径（与 SSE *-changed 事件同形）：
 * applier 的 md5 幂等守卫兜底——已是最新则只触发一次无害读取、不重复广播 / 落盘（合法 no-op）。
 * 未启用文件树 / 覆盖集子系统的路径自身已内部短路（applier 为 null 直接返回），无需在此判空。
 *
 * **须在 async 线程调用**（内部 HTTP / 文件 IO 均阻塞）：由命令执行器在 async 线程触发，
 * 故此处不再额外起线程、也绝不上 MC 主线程。
 *
 * @return true=已执行三条重拉；false=因未运行/正在停机跳过（调用方据此回传 failed，不误报 done）
 */
fun AgentLifecycle.forceResyncNow(): Boolean {
    if (!running.get()) return false
    adapter.info("执行强制重同步：重拉有效配置/文件树/覆盖集")
    fetchAndApplyConfigOnce()
    fetchAndApplyFileTreeOnce()
    fetchAndApplyOverrideOnce()
    return true
}

/**
 * 立即重同步文件树（运维 resync）：以 fileTreeMd5=null 强制一次清单拉取并由 applier 幂等应用，
 * 旁路文件树长轮询 304，不等超时。
 *
 * 复用 FileTreeApplier 的 fileTreeMd5 幂等守卫：清单未变则只触发一次无害读取、不重复落盘。
 * 独立一发，不接管文件树长轮询主循环、不改其代标识。
 *
 * @return true 表示文件树子系统已启用、已触发同步；false 表示未启用（fileTreeApplier 为 null），未触发。
 */
fun AgentLifecycle.forceSyncFileTreeNow(): Boolean {
    if (!running.get()) return false
    val applierLocal = fileTreeApplier ?: return false
    adapter.info("收到 resync：强制立刻重拉文件清单并同步落盘")
    adapter.runAsync {
        when (val result = apiClient.pollFileManifest(identity, currentMd5 = null, timeoutMs = settings.requestTimeoutMs)) {
            is FileManifestPollResult.Changed -> applierLocal.apply(result.manifest)
            is FileManifestPollResult.NotModified -> adapter.info("resync 完成：文件树无变更")
            is FileManifestPollResult.NotRegistered -> {
                adapter.warn("resync 时返回未注册，触发重新接入")
                triggerReregister()
            }

            is FileManifestPollResult.Failed -> adapter.warn("resync 强制重拉文件清单失败（${result.reason}），保留本地镜像不动")
        }
    }
    return true
}
