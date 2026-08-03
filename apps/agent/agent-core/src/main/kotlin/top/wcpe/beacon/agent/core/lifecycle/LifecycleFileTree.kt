package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.FileManifestPollResult
import top.wcpe.beacon.agent.core.client.pollFileManifest

// ---- 文件树长轮询循环（通道B，与配置长轮询并行、唤醒集合独立） ----

/** 启动文件树长轮询循环；未启用文件树（fileTreeApplier 为 null）则不启。 */
internal fun AgentLifecycle.startFileTreePollLoop() {
    if (fileTreeApplier == null) return
    val gen = fileTreeGen.get() + 1
    fileTreeGen.set(gen)
    scheduleFileTreePoll(gen, 0)
}

internal fun AgentLifecycle.scheduleFileTreePoll(
    gen: Int,
    delayMs: Long,
) {
    if (!running.get()) return
    if (delayMs <= 0) {
        adapter.runAsync { fileTreeTick(gen) }
    } else {
        adapter.runAsyncDelayed(delayMs) { fileTreeTick(gen) }
    }
}

internal fun AgentLifecycle.fileTreeTick(gen: Int) {
    val applierLocal = fileTreeApplier ?: return
    if (!running.get() || gen != fileTreeGen.get()) return
    // 当前 fileTreeMd5 取本地已落盘清单（首启无清单则空，强制首拉）。
    val currentMd5 = applierLocal.currentFileTreeMd5()
    when (val result = apiClient.pollFileManifest(identity, currentMd5, settings.pollTimeoutMs)) {
        is FileManifestPollResult.Changed -> {
            // 200：差分增量同步并镜像落盘；fail-static 由 applier 内部把控（取内容失败不删既有）。
            applierLocal.apply(result.manifest)
            fileTreeBackoff.reset()
            scheduleFileTreePoll(gen, 0)
        }

        is FileManifestPollResult.NotModified -> {
            // 304：用旧 fileTreeMd5 立即续杯，不退避。
            fileTreeBackoff.reset()
            scheduleFileTreePoll(gen, 0)
        }

        is FileManifestPollResult.NotRegistered -> {
            adapter.warn("文件树长轮询返回未注册，触发重新注册")
            triggerReregister()
        }

        is FileManifestPollResult.Failed -> {
            // 连接级失败：fail-static——不动任何已落盘文件，退避后重连。
            val delay = fileTreeBackoff.nextDelayMs()
            adapter.warn("文件树长轮询连接失败（${result.reason}），保留本地镜像不动，${delay}ms 后重连")
            scheduleFileTreePoll(gen, delay)
        }
    }
}

/** file-changed：以当前本地 fileTreeMd5 拉一次 files/manifest 并增量同步落盘（fail-static 由 applier 内部把控）。 */
internal fun AgentLifecycle.fetchAndApplyFileTreeOnce() {
    val applierLocal = fileTreeApplier ?: return
    when (val result = apiClient.pollFileManifest(identity, applierLocal.currentFileTreeMd5(), settings.requestTimeoutMs)) {
        is FileManifestPollResult.Changed -> {
            applierLocal.apply(result.manifest)
            markRunningAfterStreamSuccess()
        }

        is FileManifestPollResult.NotModified -> markRunningAfterStreamSuccess()
        is FileManifestPollResult.NotRegistered -> {
            adapter.warn("SSE 取文件清单返回未注册，触发重新注册")
            triggerReregister()
        }

        is FileManifestPollResult.Failed -> adapter.warn("SSE 取文件清单失败（${result.reason}），保留本地镜像不动，待下次事件/重连")
    }
}
