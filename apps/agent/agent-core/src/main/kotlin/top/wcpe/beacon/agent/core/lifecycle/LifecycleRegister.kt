package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.RegisterOutcome
import top.wcpe.beacon.agent.core.client.RegisterResult
import top.wcpe.beacon.agent.core.client.register

// ---- 内部流程 ----

internal fun AgentLifecycle.applySnapshotIfPresent() {
    val snapshot = snapshotStore?.read() ?: return
    applier.apply(snapshot)
    adapter.info("已从本地快照点亮有效配置，md5=${snapshot.md5}")
}

/**
 * 重新注册触发点（心跳 404 / 长轮询 404 / reload 时 404 通用入口）：
 * 递增注册代作废旧退避链，再经单飞门发起注册。
 */
internal fun AgentLifecycle.triggerReregister() {
    if (!running.get()) return
    val gen = registerGen.updateAndGet { it + 1 }
    beginRegister(gen)
}

/**
 * 注册单飞入口：CAS 抢占注册门，抢到且代未过期才执行注册；否则 no-op。
 *
 * 单飞门 [registering] 保证任意时刻只有一条 register→loops 在飞；
 * [registerGen] 保证延迟退避重试携带的旧代在新接入发起后自我作废。
 */
internal fun AgentLifecycle.beginRegister(gen: Int) {
    if (!running.get()) return
    // 抢不到门：已有一条注册在飞，本次直接放弃（单飞）。
    if (!registering.compareAndSet(false, true)) return
    // 抢到门后再校验代：本次触发已被更新的代取代（如刚发起过更晚的 reconnect）→ 释放门作废。
    if (gen != registerGen.get() || !running.get()) {
        registering.set(false)
        return
    }
    doRegister()
}

internal fun AgentLifecycle.doRegister() {
    state.set(AgentState.REGISTERING)
    when (val outcome = apiClient.register(identity, currentBackends())) {
        is RegisterOutcome.Success -> onRegisterSuccess(outcome.result)
        is RegisterOutcome.PendingApproval -> pendingRegistration.waitForApproval(outcome)
        is RegisterOutcome.ActiveBindingConfirmed -> {
            stopForAuthority("active runtime 收到未完成的数据面绑定结果")
        }
        is RegisterOutcome.DuplicateServerId -> {
            adapter.error("注册被拒：重复的 serverId（${identity.serverId}），请检查部署是否冲突", null)
            degradeAndRetryRegister()
        }

        is RegisterOutcome.OfflineRejected -> enterOfflineAndProbe()

        is RegisterOutcome.Disabled -> {
            stopForAuthority("身份已确认但被后台禁用：${identity.serverId}")
        }

        is RegisterOutcome.Rejected -> {
            stopForAuthority("身份申请已被后台拒绝：${identity.serverId}")
        }

        is RegisterOutcome.IdentityConflict -> {
            stopForAuthority("身份处于冲突态：${identity.serverId}")
        }

        is RegisterOutcome.Unbound -> stopForAuthority("身份绑定已被控制面解除：${identity.serverId}")

        is RegisterOutcome.Unauthorized -> {
            adapter.error("注册被拒：X-Beacon-Token 缺失或错误", null)
            degradeAndRetryRegister()
        }

        is RegisterOutcome.IdentityRequired -> {
            adapter.error("注册被拒：身份缺失（serverId/namespace）", null)
            degradeAndRetryRegister()
        }

        is RegisterOutcome.Failed -> {
            adapter.warn("注册失败（${outcome.reason}），按本地快照降级运行并退避重试")
            degradeAndRetryRegister()
        }
    }
}

internal fun AgentLifecycle.onRegisterSuccess(result: RegisterResult) {
    registerBackoff.reset()
    if (result.heartbeatIntervalSec > 0) {
        heartbeatIntervalMs = result.heartbeatIntervalSec * 1000L
    }
    state.set(AgentState.RUNNING)
    adapter.info(
        "注册成功：group=${result.resolvedGroup ?: "-"}，zone=${result.resolvedZone ?: "未指派"}，" +
            "心跳周期=${result.heartbeatIntervalSec}s",
    )
    startHeartbeatLoop()
    // 周期性把负载指标刷进控制面注册表（FR-32 / FR-34）：与配置变更解耦，稳态 304 时仍持续上报真值。
    startMetricsReportLoop()
    // v2 指标 1s 采样 + 5s 批上报（FR-144）：启用时随注册成功启动；缓冲跨重连保留支撑断连补报。
    if (metricsSamplingEnabled.get()) {
        metricsSampling.start()
    }
    // 调度候选刷新循环（FR-148）：随注册成功启动（幂等，重注册不重启）；候选快照跨重连保留、恢复后自动补报降级决策。
    schedulingRuntime?.start()
    // 文件资产索引周期扫描（FR-163）：随注册成功启动（幂等，重注册不重启）；扫描 + 上报全在 async 线程，fail-static。
    assetScan?.start()
    // 注入了 streamTransport（FR-24）：以单条 SSE 推送流取代三条长轮询；否则退回三条长轮询（迁移期兼容）。
    if (apiClient.streamingEnabled()) {
        startStreamLoop()
    } else {
        startConfigPollLoop()
        startFileTreePollLoop()
        startOverridePollLoop()
    }
    // 循环已启，本次注册收尾，释放单飞门。
    registering.set(false)
    // 首次注册成功放行就绪等待者（countDown 幂等，后续注册无副作用）。
    firstRegisterLatch.countDown()
    registeredListeners.forEach { listener ->
        try {
            listener()
        } catch (e: Exception) {
            adapter.warn("注册成功监听器执行失败：${e.message}")
        }
    }
}

/** 进降级态并退避后重试注册（保留快照、不阻断玩家）。 */
internal fun AgentLifecycle.degradeAndRetryRegister() {
    state.set(AgentState.DEGRADED)
    // 先记下本次注册所属的代，释放单飞门，再安排延迟重试（重试 fire 时按此代校验，过期则作废）。
    val gen = registerGen.get()
    registering.set(false)
    if (!running.get()) return
    val delay = registerBackoff.nextDelayMs()
    adapter.runAsyncDelayed(delay) { beginRegister(gen) }
}

internal fun AgentLifecycle.stopForAuthority(message: String) {
    adapter.warn("$message，停止 active runtime")
    shutdown()
    authorityInvalidated()
}

/**
 * 被控制面主动下线（FR-49）：进 OFFLINE 态，停止退避猛打，改按大间隔降频探测重注册。
 *
 * 与 DEGRADED（控制面不可用）严格区分：日志只在**首次进入 OFFLINE** 时 WARN 一次、
 * 后续降频探测仍被拒不再打日志，不刷屏；保留快照、不阻断玩家（fail-static 不变）。
 * 取消下线后下一次降频探测即注册成功、回 RUNNING（运维亦可经 reconnect 立即拉起）。
 */
internal fun AgentLifecycle.enterOfflineAndProbe() {
    // 首次进入 OFFLINE 才 WARN 一次；后续重复被拒静默（仅安排下一次降频探测），避免刷屏。
    val firstEntry = state.getAndSet(AgentState.OFFLINE) != AgentState.OFFLINE
    if (firstEntry) {
        adapter.warn("注册被拒：本实例已被控制面主动下线（${identity.serverId}）；停止重连，按降频探测等待取消下线")
    }
    // 记下本次注册所属代，释放单飞门，再按大间隔安排一次降频探测（fire 时按此代校验，过期则作废）。
    val gen = registerGen.get()
    registering.set(false)
    if (!running.get()) return
    adapter.runAsyncDelayed(settings.offlineProbeIntervalMs) { beginRegister(gen) }
}
