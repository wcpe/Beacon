package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.backoff.ExponentialBackoff
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.FileManifestPollResult
import top.wcpe.beacon.agent.core.client.HeartbeatOutcome
import top.wcpe.beacon.agent.core.client.OverridePollResult
import top.wcpe.beacon.agent.core.client.PollResult
import top.wcpe.beacon.agent.core.client.RegisterOutcome
import top.wcpe.beacon.agent.core.client.RegisterResult
import top.wcpe.beacon.agent.core.config.ConfigApplier
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.filetree.FileTreeApplier
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.override.OverrideSyncApplier
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.snapshot.SnapshotStore
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicReference

/**
 * agent 生命周期编排：BOOTSTRAP → REGISTERING → RUNNING → DEGRADED。
 *
 * 全程异步经 PlatformAdapter，绝不阻塞 MC 主线程；HTTP / 文件 IO 不上主线程。
 * 任何阶段都不阻断玩家进服（fail-static）。
 */
class AgentLifecycle(
    private val identity: AgentIdentity,
    private val settings: AgentSettings,
    private val adapter: PlatformAdapter,
    private val apiClient: BeaconApiClient,
    private val store: EffectiveConfigStore,
    private val applier: ConfigApplier,
    private val snapshotStore: SnapshotStore?,
    private val fileTreeApplier: FileTreeApplier? = null,
    private val overrideApplier: OverrideSyncApplier? = null,
) {

    private val state = AtomicReference(AgentState.BOOTSTRAP)

    /** 总运行标志：shutdown 后置 false，所有循环据此停转。 */
    private val running = AtomicBoolean(false)

    /** 心跳与长轮询各自独立的「代」标识：重启循环时递增，旧循环自然退出。 */
    private val heartbeatGen = AtomicReference(0)
    private val pollGen = AtomicReference(0)

    /**
     * 文件树长轮询（通道B）的「代」标识：与配置长轮询并行、各自 gen，重启循环时递增。
     * 唤醒集合与配置独立（fileTreeMd5 ≠ 配置 md5，见 ADR-0010）。
     */
    private val fileTreeGen = AtomicReference(0)

    /**
     * 三方覆盖集长轮询（FR-15）的「代」标识：与配置 / 文件树长轮询并行、各自 gen，重启循环时递增。
     * overrideMd5 维度独立（≠ 配置 md5 / fileTreeMd5，见 ADR-0011）。
     */
    private val overrideGen = AtomicReference(0)

    /**
     * 注册单飞门：任意时刻只允许一条 register→loops 在飞。
     * 多触发点（心跳 404 / 长轮询 404 / 退避重试 / reconnectNow）并发抢占，CAS 失败者直接 no-op，
     * 杜绝瞬时双注册、双循环。
     */
    private val registering = AtomicBoolean(false)

    /**
     * 注册「代」标识：reconnectNow 与各重新注册触发点递增；延迟退避重试携带触发时的代，
     * fire 时代不符即自我作废——杜绝旧退避链与新接入链并存。
     */
    private val registerGen = AtomicReference(0)

    /** 心跳周期（毫秒）：注册成功前用兜底值，成功后用下发值。 */
    @Volatile
    private var heartbeatIntervalMs: Long = settings.heartbeatFallbackMs

    private val registerBackoff = ExponentialBackoff(settings.backoff)
    private val pollBackoff = ExponentialBackoff(settings.backoff)
    private val fileTreeBackoff = ExponentialBackoff(settings.backoff)
    private val overrideBackoff = ExponentialBackoff(settings.backoff)

    /** 当前是否已连上控制面（供对外 API connected() 读）。 */
    fun isConnected(): Boolean = state.get() == AgentState.RUNNING

    /** 当前状态（便于壳层 / 测试观察）。 */
    fun currentState(): AgentState = state.get()

    /**
     * 当前可观测状态快照（供壳层 status 命令渲染）。core 不持有平台类型（守 ADR-0005）。
     */
    fun snapshot(): LifecycleSnapshot = LifecycleSnapshot(
        state = state.get(),
        connected = isConnected(),
        effectiveMd5 = store.currentMd5(),
        heartbeatIntervalSec = (heartbeatIntervalMs / 1000L).toInt(),
        endpoint = settings.primaryEndpoint(),
    )

    /**
     * 立即重连（运维 reconnect）：打断退避、重置、重新接入控制面。
     *
     * 幂等 + 线程安全：经单飞门，并发多次调用不会叠加出多条 register→loops；
     * **不清空 store / 快照**——保 fail-static，重连期间玩家仍按当前有效配置运行。
     */
    fun reconnectNow() {
        if (!running.get()) return
        // 递增注册代：作废仍在排队的旧退避重试，避免新旧接入链并存。
        val gen = registerGen.updateAndGet { it + 1 }
        registerBackoff.reset()
        pollBackoff.reset()
        fileTreeBackoff.reset()
        overrideBackoff.reset()
        // 递增循环代，使在跑的旧心跳 / 长轮询 / 文件树 / 覆盖集循环在下一跳自然退出（新循环由本次注册成功后重启）。
        heartbeatGen.set(heartbeatGen.get() + 1)
        pollGen.set(pollGen.get() + 1)
        fileTreeGen.set(fileTreeGen.get() + 1)
        overrideGen.set(overrideGen.get() + 1)
        adapter.info("收到 reconnect：重置退避并重新接入控制面（保留当前有效配置）")
        adapter.runAsync { beginRegister(gen) }
    }

    /**
     * 立即重拉有效配置（运维 reload）：以 md5=null 强制一次拉取并 apply，旁路长轮询 304，不等超时。
     *
     * 复用 ConfigApplier 的 md5 幂等守卫：内容未变则只触发一次无害读取、不重复广播。
     * 独立一发，不接管长轮询主循环、不改其代标识。
     */
    fun forcePollNow() {
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
     * 启动：读快照→有则先 apply 点亮有效配置→再异步注册→成功后启心跳 + 长轮询。
     * 全程不阻塞调用线程（壳层在 ENABLE 调用，内部即转异步）。
     */
    fun bootstrapWithSnapshotThenConnect() {
        running.set(true)
        adapter.runAsync {
            // 1) 先点亮本地快照，玩家此刻已可进服。
            applySnapshotIfPresent()
            // 2) 再异步注册并启循环（经单飞门）。
            beginRegister(registerGen.get())
        }
    }

    /** 停止：置 running=false，递增代标识使所有循环在下一跳退出。 */
    fun shutdown() {
        running.set(false)
        heartbeatGen.set(heartbeatGen.get() + 1)
        pollGen.set(pollGen.get() + 1)
        fileTreeGen.set(fileTreeGen.get() + 1)
        overrideGen.set(overrideGen.get() + 1)
        adapter.info("agent 生命周期已停止")
    }

    // ---- 内部流程 ----

    private fun applySnapshotIfPresent() {
        val snapshot = snapshotStore?.read() ?: return
        applier.apply(snapshot)
        adapter.info("已从本地快照点亮有效配置，md5=${snapshot.md5}")
    }

    /**
     * 重新注册触发点（心跳 404 / 长轮询 404 / reload 时 404 通用入口）：
     * 递增注册代作废旧退避链，再经单飞门发起注册。
     */
    private fun triggerReregister() {
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
    private fun beginRegister(gen: Int) {
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

    private fun doRegister() {
        state.set(AgentState.REGISTERING)
        when (val outcome = apiClient.register(identity)) {
            is RegisterOutcome.Success -> onRegisterSuccess(outcome.result)
            is RegisterOutcome.DuplicateServerId -> {
                adapter.error("注册被拒：重复的 serverId（${identity.serverId}），请检查部署是否冲突", null)
                degradeAndRetryRegister()
            }

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

    private fun onRegisterSuccess(result: RegisterResult) {
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
        startConfigPollLoop()
        startFileTreePollLoop()
        startOverridePollLoop()
        // 循环已启，本次注册收尾，释放单飞门。
        registering.set(false)
    }

    /** 进降级态并退避后重试注册（保留快照、不阻断玩家）。 */
    private fun degradeAndRetryRegister() {
        state.set(AgentState.DEGRADED)
        // 先记下本次注册所属的代，释放单飞门，再安排延迟重试（重试 fire 时按此代校验，过期则作废）。
        val gen = registerGen.get()
        registering.set(false)
        if (!running.get()) return
        val delay = registerBackoff.nextDelayMs()
        adapter.runAsyncDelayed(delay) { beginRegister(gen) }
    }

    // ---- 心跳循环 ----

    private fun startHeartbeatLoop() {
        val gen = heartbeatGen.get() + 1
        heartbeatGen.set(gen)
        scheduleHeartbeat(gen, heartbeatIntervalMs)
    }

    private fun scheduleHeartbeat(gen: Int, delayMs: Long) {
        if (!running.get()) return
        adapter.runAsyncDelayed(delayMs) { heartbeatTick(gen) }
    }

    private fun heartbeatTick(gen: Int) {
        // 代标识不符（已重启循环或已 shutdown）→ 当前跳作废。
        if (!running.get() || gen != heartbeatGen.get()) return
        when (apiClient.heartbeat(identity)) {
            is HeartbeatOutcome.Ok -> scheduleHeartbeat(gen, heartbeatIntervalMs)
            is HeartbeatOutcome.NotRegistered -> {
                adapter.warn("心跳返回未注册，触发重新注册")
                // 重新注册会重启两条循环，本代心跳到此为止；经单飞门，与其它触发点互斥。
                triggerReregister()
            }

            is HeartbeatOutcome.Failed -> {
                // 心跳连接失败不进 DEGRADED（长轮询循环负责连接级降级判定），按周期重试即可。
                scheduleHeartbeat(gen, heartbeatIntervalMs)
            }
        }
    }

    // ---- 长轮询循环 ----

    private fun startConfigPollLoop() {
        val gen = pollGen.get() + 1
        pollGen.set(gen)
        schedulePoll(gen, 0)
    }

    private fun schedulePoll(gen: Int, delayMs: Long) {
        if (!running.get()) return
        if (delayMs <= 0) {
            adapter.runAsync { pollTick(gen) }
        } else {
            adapter.runAsyncDelayed(delayMs) { pollTick(gen) }
        }
    }

    private fun pollTick(gen: Int) {
        if (!running.get() || gen != pollGen.get()) return
        val currentMd5 = store.currentMd5()
        when (val result = apiClient.pollEffective(identity, currentMd5, settings.pollTimeoutMs)) {
            is PollResult.Changed -> {
                // 200：apply（含写快照、广播）→ report → 用新 md5 续杯。
                applier.apply(result.effective)
                reportApplied(result.effective.md5)
                markRunningAfterPollSuccess()
                schedulePoll(gen, 0)
            }

            is PollResult.NotModified -> {
                // 304：用旧 md5 立即续杯，不退避。
                markRunningAfterPollSuccess()
                schedulePoll(gen, 0)
            }

            is PollResult.NotRegistered -> {
                adapter.warn("长轮询返回未注册，触发重新注册")
                triggerReregister()
            }

            is PollResult.Failed -> {
                // 连接级失败：进 DEGRADED，保持当前有效配置不回退，退避后重连。
                state.set(AgentState.DEGRADED)
                val delay = pollBackoff.nextDelayMs()
                adapter.warn("长轮询连接失败（${result.reason}），保持当前有效配置，${delay}ms 后重连")
                schedulePoll(gen, delay)
            }
        }
    }

    /** 长轮询成功一轮：重置退避并回到 RUNNING（从 DEGRADED 恢复）。 */
    private fun markRunningAfterPollSuccess() {
        pollBackoff.reset()
        if (state.get() == AgentState.DEGRADED) {
            state.set(AgentState.RUNNING)
            adapter.info("已重连控制面，恢复 RUNNING")
        }
    }

    private fun reportApplied(appliedMd5: String) {
        // 上报失败仅告警，不影响主流程（playerCount/tps 由壳层若有则注入，这里 MVP 报 0）。
        val ok = apiClient.report(identity, appliedMd5, playerCount = 0, tps = 0.0)
        if (!ok) {
            adapter.warn("上报 applied 状态失败（不影响有效配置生效）")
        }
    }

    // ---- 文件树长轮询循环（通道B，与配置长轮询并行、唤醒集合独立） ----

    /** 启动文件树长轮询循环；未启用文件树（fileTreeApplier 为 null）则不启。 */
    private fun startFileTreePollLoop() {
        if (fileTreeApplier == null) return
        val gen = fileTreeGen.get() + 1
        fileTreeGen.set(gen)
        scheduleFileTreePoll(gen, 0)
    }

    private fun scheduleFileTreePoll(gen: Int, delayMs: Long) {
        if (!running.get()) return
        if (delayMs <= 0) {
            adapter.runAsync { fileTreeTick(gen) }
        } else {
            adapter.runAsyncDelayed(delayMs) { fileTreeTick(gen) }
        }
    }

    private fun fileTreeTick(gen: Int) {
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

    // ---- 三方覆盖集长轮询循环（FR-15，与配置 / 文件树长轮询并行、md5 维度独立） ----

    /** 启动覆盖集长轮询循环；未启用覆盖集接线（overrideApplier 为 null）则不启。 */
    private fun startOverridePollLoop() {
        if (overrideApplier == null) return
        val gen = overrideGen.get() + 1
        overrideGen.set(gen)
        scheduleOverridePoll(gen, 0)
    }

    private fun scheduleOverridePoll(gen: Int, delayMs: Long) {
        if (!running.get()) return
        if (delayMs <= 0) {
            adapter.runAsync { overrideTick(gen) }
        } else {
            adapter.runAsyncDelayed(delayMs) { overrideTick(gen) }
        }
    }

    private fun overrideTick(gen: Int) {
        val applierLocal = overrideApplier ?: return
        if (!running.get() || gen != overrideGen.get()) return
        // 当前 overrideMd5 取本地已收敛那一版（首启 / 上轮有集失败则为 null，强制重拉重做）。
        val currentMd5 = applierLocal.currentOverrideMd5()
        when (val result = apiClient.pollOverrideSets(identity, currentMd5, settings.pollTimeoutMs)) {
            is OverridePollResult.Changed -> {
                // 200：逐集落 targetRoot（备份 + 安全校验 + 受管标记）→ 命中白名单才派发重载命令。
                // fail-static 由 applier 内部把控（取内容失败 / 恶意 targetRoot 不动既有、不派发）。
                applierLocal.apply(result.manifest)
                overrideBackoff.reset()
                scheduleOverridePoll(gen, 0)
            }

            is OverridePollResult.NotModified -> {
                // 304：用旧 overrideMd5 立即续杯，不退避。
                overrideBackoff.reset()
                scheduleOverridePoll(gen, 0)
            }

            is OverridePollResult.NotRegistered -> {
                adapter.warn("覆盖集长轮询返回未注册，触发重新注册")
                triggerReregister()
            }

            is OverridePollResult.Failed -> {
                // 连接级失败：fail-static——不动任何已落盘文件、不派发命令，退避后重连。
                val delay = overrideBackoff.nextDelayMs()
                adapter.warn("覆盖集长轮询连接失败（${result.reason}），保留本地覆盖不动，${delay}ms 后重连")
                scheduleOverridePoll(gen, delay)
            }
        }
    }
}
