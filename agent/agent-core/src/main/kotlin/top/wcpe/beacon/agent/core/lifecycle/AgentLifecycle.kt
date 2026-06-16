package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.backoff.ExponentialBackoff
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.HeartbeatOutcome
import top.wcpe.beacon.agent.core.client.PollResult
import top.wcpe.beacon.agent.core.client.RegisterOutcome
import top.wcpe.beacon.agent.core.client.RegisterResult
import top.wcpe.beacon.agent.core.config.ConfigApplier
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.identity.AgentIdentity
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
) {

    private val state = AtomicReference(AgentState.BOOTSTRAP)

    /** 总运行标志：shutdown 后置 false，所有循环据此停转。 */
    private val running = AtomicBoolean(false)

    /** 心跳与长轮询各自独立的「代」标识：重启循环时递增，旧循环自然退出。 */
    private val heartbeatGen = AtomicReference(0)
    private val pollGen = AtomicReference(0)

    /** 心跳周期（毫秒）：注册成功前用兜底值，成功后用下发值。 */
    @Volatile
    private var heartbeatIntervalMs: Long = settings.heartbeatFallbackMs

    private val registerBackoff = ExponentialBackoff(settings.backoff)
    private val pollBackoff = ExponentialBackoff(settings.backoff)

    /** 当前是否已连上控制面（供对外 API connected() 读）。 */
    fun isConnected(): Boolean = state.get() == AgentState.RUNNING

    /** 当前状态（便于壳层 / 测试观察）。 */
    fun currentState(): AgentState = state.get()

    /**
     * 启动：读快照→有则先 apply 点亮有效配置→再异步注册→成功后启心跳 + 长轮询。
     * 全程不阻塞调用线程（壳层在 ENABLE 调用，内部即转异步）。
     */
    fun bootstrapWithSnapshotThenConnect() {
        running.set(true)
        adapter.runAsync {
            // 1) 先点亮本地快照，玩家此刻已可进服。
            applySnapshotIfPresent()
            // 2) 再异步注册并启循环。
            registerThenStartLoops()
        }
    }

    /** 停止：置 running=false，递增代标识使所有循环在下一跳退出。 */
    fun shutdown() {
        running.set(false)
        heartbeatGen.set(heartbeatGen.get() + 1)
        pollGen.set(pollGen.get() + 1)
        adapter.info("agent 生命周期已停止")
    }

    // ---- 内部流程 ----

    private fun applySnapshotIfPresent() {
        val snapshot = snapshotStore?.read() ?: return
        applier.apply(snapshot)
        adapter.info("已从本地快照点亮有效配置，md5=${snapshot.md5}")
    }

    private fun registerThenStartLoops() {
        if (!running.get()) return
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
    }

    /** 进降级态并退避后重试注册（保留快照、不阻断玩家）。 */
    private fun degradeAndRetryRegister() {
        if (!running.get()) return
        state.set(AgentState.DEGRADED)
        val delay = registerBackoff.nextDelayMs()
        adapter.runAsyncDelayed(delay) { registerThenStartLoops() }
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
                // 重新注册会重启两条循环，本代心跳到此为止。
                registerThenStartLoops()
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
                registerThenStartLoops()
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
}
