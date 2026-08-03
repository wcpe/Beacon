package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.ReportedChannelMd5
import top.wcpe.beacon.agent.core.stream.StreamEventTypes
import top.wcpe.beacon.agent.core.transport.StreamEvent
import top.wcpe.beacon.agent.core.transport.StreamListener

// ---- SSE 推送流循环（FR-24，注入 streamTransport 时取代上面三条长轮询） ----

/** 启动单条 SSE 推送流循环：合并三通道变更通知 + 连接即对账（见 ADR-0015）。 */
internal fun AgentLifecycle.startStreamLoop() {
    val gen = streamGen.get() + 1
    streamGen.set(gen)
    scheduleStream(gen, 0)
}

internal fun AgentLifecycle.scheduleStream(
    gen: Int,
    delayMs: Long,
) {
    if (!running.get()) return
    if (delayMs <= 0) {
        adapter.runAsync { streamConnect(gen) }
    } else {
        adapter.runAsyncDelayed(delayMs) { streamConnect(gen) }
    }
}

/**
 * 建立一条 SSE 流并阻塞读取：上报各通道当前 md5（连接即对账），逐事件触发取数据-应用。
 * 流结束（断线 / 关停）后按退避重连——重连即再次对账补增量，不丢更新、fail-static。
 */
internal fun AgentLifecycle.streamConnect(gen: Int) {
    if (!running.get() || gen != streamGen.get()) return
    // 上报各通道本地当前 md5（空串=本地无该通道内容，控制面补全量）。
    // 拓扑通道 agent 不本地维护摘要，恒上报空串，让控制面在连接即对账时补一次 topology-changed（FR-29）。
    val reported =
        ReportedChannelMd5(
            config = store.currentMd5() ?: "",
            file = fileTreeApplier?.currentFileTreeMd5() ?: "",
            override = overrideApplier?.currentOverrideMd5() ?: "",
            topology = "",
        )
    apiClient.openStream(identity, reported, StreamLoopListener(this, gen))
}

/**
 * SSE 事件分发：按事件类型触发对应通道的强制重取-应用（复用现有 HTTP 端点逻辑，见 ADR-0015 决策 2）。
 *
 * 事件 data 行携带的 md5 仅作"有变更"通知、agent 不消费它——*-changed 一律忽略 event.data，
 * 改用本地已应用的 md5 走现有端点重拉（端点比对 md5，真变才返 200），故此处不解析载荷。
 */
internal fun AgentLifecycle.dispatchStreamEvent(
    gen: Int,
    event: StreamEvent,
) {
    if (!running.get() || gen != streamGen.get()) return
    when (event.type) {
        StreamEventTypes.READY -> {
            adapter.info("SSE 连接即对账完成，转入直播推送")
            // 兜住断连期间排进来的命令：READY 后主动拉一次待办命令（与 command-pending 事件单飞去重）。
            triggerReverseFetch()
        }

        StreamEventTypes.CONFIG_CHANGED -> fetchAndApplyConfigOnce()
        StreamEventTypes.FILE_CHANGED -> fetchAndApplyFileTreeOnce()
        StreamEventTypes.OVERRIDE_CHANGED -> fetchAndApplyOverrideOnce()
        StreamEventTypes.TOPOLOGY_CHANGED -> fireTopologyChanged()
        StreamEventTypes.COMMAND_PENDING -> triggerReverseFetch()
        else -> adapter.warn("收到未知 SSE 事件类型：${event.type}（忽略）")
    }
}

/**
 * 触发反向抓取（FR-39）：在 async 适配器线程拉待办命令并执行（读 plugins → 回传），绝不上 MC 主线程。
 *
 * 未装配执行器（reverseFetchExecutor 为 null）则 no-op。executor 内部单飞去重：command-pending 与 READY
 * 并发触发只会跑一条抓取流。独立一发到 async 线程，不阻塞 SSE 事件分发。
 */
internal fun AgentLifecycle.triggerReverseFetch() {
    val executor = reverseFetchExecutor ?: return
    adapter.runAsync { executor.trigger() }
}

/** 流结束处理：进 DEGRADED（连接级降级）、保留本地快照，退避后重连（重连即再次对账）。 */
internal fun AgentLifecycle.onStreamClosed(
    gen: Int,
    error: Throwable?,
) {
    if (!running.get() || gen != streamGen.get()) return
    state.set(AgentState.DEGRADED)
    val delay = streamBackoff.nextDelayMs()
    val reason = error?.message ?: "正常关闭"
    adapter.warn("SSE 推送流断开（$reason），保持当前有效配置，${delay}ms 后重连并对账")
    scheduleStream(gen, delay)
}

/**
 * topology-changed（FR-29）：控制面只发"拓扑变了"通知、不搬实例数据，故此处不取数据，
 * 仅回调拓扑监听器，由业务侧自行重查发现端点取最新拓扑。
 */
internal fun AgentLifecycle.fireTopologyChanged() {
    val listener = topologyListener ?: return
    try {
        listener()
    } catch (e: Exception) {
        adapter.warn("拓扑监听器回调执行失败：${e.message}")
    }
}

/** SSE 取数据成功一轮：重置流退避并从 DEGRADED 恢复 RUNNING（健康判活仍由心跳决定，与此解耦）。 */
internal fun AgentLifecycle.markRunningAfterStreamSuccess() {
    streamBackoff.reset()
    if (state.get() == AgentState.DEGRADED) {
        state.set(AgentState.RUNNING)
        adapter.info("SSE 推送流已恢复，回到 RUNNING")
    }
}

/** SSE 流监听器：把 transport 回调桥接到生命周期的事件分发与重连，携带流代标识自我作废过期回调。 */
private class StreamLoopListener(
    private val lifecycle: AgentLifecycle,
    private val gen: Int,
) : StreamListener {
    override fun onOpen() {
        if (!lifecycle.running.get() || gen != lifecycle.streamGen.get()) return
        lifecycle.streamBackoff.reset()
        lifecycle.adapter.info("SSE 推送流已建立，开始连接即对账")
    }

    override fun onEvent(event: StreamEvent) {
        // 事件处理放异步线程，不阻塞 transport 的读流线程（取数据-应用本身可能含 IO）。
        lifecycle.adapter.runAsync { lifecycle.dispatchStreamEvent(gen, event) }
    }

    override fun onClosed(error: Throwable?) {
        lifecycle.onStreamClosed(gen, error)
    }
}
