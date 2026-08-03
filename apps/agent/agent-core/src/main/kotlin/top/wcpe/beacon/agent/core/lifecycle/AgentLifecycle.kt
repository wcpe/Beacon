package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.backoff.ExponentialBackoff
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.SelfHealth
import top.wcpe.beacon.agent.core.command.ReverseFetchExecutor
import top.wcpe.beacon.agent.core.config.ConfigApplier
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.filetree.FileTreeApplier
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.metrics.ProxyMetricsProvider
import top.wcpe.beacon.agent.core.metrics.RuntimeMetricsProvider
import top.wcpe.beacon.agent.core.override.OverrideSyncApplier
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.scheduling.SchedulingRuntime
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.snapshot.SnapshotStore
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicReference

/**
 * agent 生命周期编排：BOOTSTRAP → REGISTERING → RUNNING → DEGRADED。
 *
 * 全程异步经 PlatformAdapter，绝不阻塞 MC 主线程；HTTP / 文件 IO 不上主线程。
 * 任何阶段都不阻断玩家进服（fail-static）。
 */
class AgentLifecycle(
    internal val identity: AgentIdentity,
    internal val settings: AgentSettings,
    internal val adapter: PlatformAdapter,
    internal val apiClient: BeaconApiClient,
    internal val store: EffectiveConfigStore,
    internal val applier: ConfigApplier,
    internal val snapshotStore: SnapshotStore? = null,
    // 可选钩子与供给（向后兼容：缺省 hooks 即旧行为——不启用文件树/覆盖/反向抓取/调度/资产扫描，零指标）。
    internal val hooks: AgentLifecycleHooks = AgentLifecycleHooks(),
) {
    internal val fileTreeApplier: FileTreeApplier? get() = hooks.fileTreeApplier
    internal val overrideApplier: OverrideSyncApplier? get() = hooks.overrideApplier
    internal val topologyListener: (() -> Unit)? get() = hooks.topologyListener
    internal val metricsProvider: RuntimeMetricsProvider get() = hooks.metricsProvider
    internal val backendsProvider: () -> List<String> get() = hooks.backendsProvider
    internal val proxyMetricsProvider: ProxyMetricsProvider get() = hooks.proxyMetricsProvider
    internal val reverseFetchExecutor: ReverseFetchExecutor? get() = hooks.reverseFetchExecutor
    internal val schedulingRuntime: SchedulingRuntime? get() = hooks.schedulingRuntime
    internal val selfHealthSink: (SelfHealth?) -> Unit get() = hooks.selfHealthSink
    internal val assetScan: AssetScanCoordinator? get() = hooks.assetScan
    internal val authorityInvalidated: () -> Unit get() = hooks.authorityInvalidated
    internal val state = AtomicReference(AgentState.BOOTSTRAP)

    /** 总运行标志：shutdown 后置 false，所有循环据此停转。 */
    internal val running = AtomicBoolean(false)

    /** 心跳与长轮询各自独立的「代」标识：重启循环时递增，旧循环自然退出。 */
    internal val heartbeatGen = AtomicReference(0)
    internal val pollGen = AtomicReference(0)

    /**
     * 周期性指标上报循环（FR-32 / FR-34）的「代」标识：与心跳同周期但独立，重启循环时递增、旧循环自然退出。
     * 此循环把负载指标的上报与「配置是否变更」解耦——稳态配置不变（长轮询恒 304）时仍持续把真值刷进注册表。
     */
    internal val metricsReportGen = AtomicReference(0)

    /** 是否启用 v2 指标采样与批上报（壳层经 [enableMetricsSampling] 在接入前开启；默认关，向后兼容既有测试）。 */
    internal val metricsSamplingEnabled = AtomicBoolean(false)

    /**
     * v2 指标采样 + 批上报协调器（FR-144）：拆出以免本类膨胀。采集经既有 provider（内存 / CPU / 在线 / TPS
     * 与 BC 专属），采集失败在 provider 内回退（fail-static 不变）。启用后随注册成功 start、shutdown 时 stop。
     */
    internal val metricsSampling =
        MetricsSamplingCoordinator(
            adapter = adapter,
            apiClient = apiClient,
            identity = identity,
            runtimeProvider = { currentMetrics() },
            proxyProvider = { currentProxyMetrics() },
            selfHealthSink = selfHealthSink,
        )

    /**
     * 文件树长轮询（通道B）的「代」标识：与配置长轮询并行、各自 gen，重启循环时递增。
     * 唤醒集合与配置独立（fileTreeMd5 ≠ 配置 md5，见 ADR-0010）。
     */
    internal val fileTreeGen = AtomicReference(0)

    /**
     * 三方覆盖集长轮询（FR-15）的「代」标识：与配置 / 文件树长轮询并行、各自 gen，重启循环时递增。
     * overrideMd5 维度独立（≠ 配置 md5 / fileTreeMd5，见 ADR-0011）。
     */
    internal val overrideGen = AtomicReference(0)

    /**
     * SSE 推送流（FR-24）的「代」标识：注入 streamTransport 时以本流取代上面三条长轮询，重启循环时递增。
     * 单条流合并三通道变更通知 + 连接即对账（见 ADR-0015）。
     */
    internal val streamGen = AtomicReference(0)

    /**
     * 注册单飞门：任意时刻只允许一条 register→loops 在飞。
     * 多触发点（心跳 404 / 长轮询 404 / 退避重试 / reconnectNow）并发抢占，CAS 失败者直接 no-op，
     * 杜绝瞬时双注册、双循环。
     */
    internal val registering = AtomicBoolean(false)

    /**
     * 注册「代」标识：reconnectNow 与各重新注册触发点递增；延迟退避重试携带触发时的代，
     * fire 时代不符即自我作废——杜绝旧退避链与新接入链并存。
     */
    internal val registerGen = AtomicReference(0)

    /** 首次注册成功放行闩：供下游有界等待身份就绪（zone 已回填）后再定身份。 */
    internal val firstRegisterLatch = CountDownLatch(1)

    /** 注册成功监听器：供平台壳启动依赖控制面身份的子系统。 */
    internal val registeredListeners = CopyOnWriteArrayList<() -> Unit>()

    /** 心跳周期（毫秒）：注册成功前用兜底值，成功后用下发值。 */
    @Volatile
    internal var heartbeatIntervalMs: Long = settings.heartbeatFallbackMs

    internal val pendingRegistration =
        PendingRegistrationController(
            identity = identity,
            settings = settings,
            adapter = adapter,
            apiClient = apiClient,
            runtime =
                PendingRegistrationRuntime(
                    state = state,
                    running = running,
                    registering = registering,
                ),
            actions =
                PendingRegistrationActions(
                    registerNow = { doRegister() },
                ),
        )

    internal val registerBackoff = ExponentialBackoff(settings.backoff)
    internal val pollBackoff = ExponentialBackoff(settings.backoff)
    internal val fileTreeBackoff = ExponentialBackoff(settings.backoff)
    internal val overrideBackoff = ExponentialBackoff(settings.backoff)
    internal val streamBackoff = ExponentialBackoff(settings.backoff)

    /** 当前是否已连上控制面（供对外 API connected() 读）。 */
    fun isConnected(): Boolean = state.get() == AgentState.RUNNING

    /** 当前状态（便于壳层 / 测试观察）。 */
    fun currentState(): AgentState = state.get()

    /**
     * 有界等待首次注册成功；已就绪立即返回 true，超时返回 false。
     * timeoutMillis <= 0 时不阻塞，只查当前是否已就绪。
     */
    fun awaitFirstRegister(timeoutMillis: Long): Boolean {
        if (timeoutMillis <= 0L) return firstRegisterLatch.count == 0L
        return try {
            firstRegisterLatch.await(timeoutMillis, TimeUnit.MILLISECONDS)
        } catch (e: InterruptedException) {
            // 等待被中断：恢复中断标志，返回当前就绪状态（不把中断当成就绪）。
            Thread.currentThread().interrupt()
            firstRegisterLatch.count == 0L
        }
    }

    /** 注册成功回调；每次 register 成功都会触发。 */
    fun onRegistered(listener: () -> Unit) {
        registeredListeners.add(listener)
    }

    /**
     * 启用 v2 指标 1s 采样 + 5s 批上报（FR-144，生产默认周期）。壳层在 [bootstrapWithSnapshotThenConnect]
     * **之前**调用，使注册成功时即启两条循环。须在接入前开启以避开「注册先于开启」的竞态。
     */
    fun enableMetricsSampling() {
        metricsSamplingEnabled.set(true)
    }

    /**
     * 启用 v2 指标采样并覆盖周期与桶宽（仅测试为加速用；须在接入前调用，无并发）。
     */
    fun enableMetricsSampling(
        sampleIntervalMs: Long,
        batchReportIntervalMs: Long,
        bucketMs: Long,
    ) {
        metricsSampling.configure(sampleIntervalMs, batchReportIntervalMs, bucketMs)
        metricsSamplingEnabled.set(true)
    }

    /**
     * 当前可观测状态快照（供壳层 status 命令渲染）。core 不持有平台类型（守 ADR-0005）。
     */
    fun snapshot(): LifecycleSnapshot =
        LifecycleSnapshot(
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
        streamBackoff.reset()
        // 递增循环代，使在跑的旧心跳 / 长轮询 / 文件树 / 覆盖集 / SSE 流循环在下一跳自然退出（新循环由本次注册成功后重启）。
        heartbeatGen.set(heartbeatGen.get() + 1)
        pollGen.set(pollGen.get() + 1)
        fileTreeGen.set(fileTreeGen.get() + 1)
        overrideGen.set(overrideGen.get() + 1)
        streamGen.set(streamGen.get() + 1)
        adapter.info("收到 reconnect：重置退避并重新接入控制面（保留当前有效配置）")
        adapter.runAsync { beginRegister(gen) }
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
            // 1.1) 从落盘候选快照恢复调度缓存（注册前即可 fail-static 降级决策，FR-148 §4.6 降级 step 1）。
            schedulingRuntime?.restoreSnapshot()
            // 2) 再异步注册并启循环（经单飞门）。
            beginRegister(registerGen.get())
        }
    }

    /** 停止：置 running=false，递增代标识使所有循环在下一跳退出，并停 v2 指标采样协调器。 */
    fun shutdown() {
        running.set(false)
        heartbeatGen.set(heartbeatGen.get() + 1)
        pollGen.set(pollGen.get() + 1)
        fileTreeGen.set(fileTreeGen.get() + 1)
        overrideGen.set(overrideGen.get() + 1)
        streamGen.set(streamGen.get() + 1)
        metricsSampling.stop()
        schedulingRuntime?.stop()
        assetScan?.stop()
        adapter.info("agent 生命周期已停止")
    }
}
