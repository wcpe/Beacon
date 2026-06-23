package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.backoff.ExponentialBackoff
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.FileManifestPollResult
import top.wcpe.beacon.agent.core.client.HeartbeatOutcome
import top.wcpe.beacon.agent.core.client.OverridePollResult
import top.wcpe.beacon.agent.core.client.PollResult
import top.wcpe.beacon.agent.core.client.RegisterOutcome
import top.wcpe.beacon.agent.core.client.RegisterResult
import top.wcpe.beacon.agent.core.client.ReportedChannelMd5
import top.wcpe.beacon.agent.core.command.ReverseFetchExecutor
import top.wcpe.beacon.agent.core.config.ConfigApplier
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.filetree.FileTreeApplier
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.metrics.ProxyMetrics
import top.wcpe.beacon.agent.core.metrics.ProxyMetricsProvider
import top.wcpe.beacon.agent.core.metrics.RuntimeMetrics
import top.wcpe.beacon.agent.core.metrics.RuntimeMetricsProvider
import top.wcpe.beacon.agent.core.override.OverrideSyncApplier
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.snapshot.SnapshotStore
import top.wcpe.beacon.agent.core.stream.StreamEventTypes
import top.wcpe.beacon.agent.core.transport.StreamEvent
import top.wcpe.beacon.agent.core.transport.StreamListener
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
    private val identity: AgentIdentity,
    private val settings: AgentSettings,
    private val adapter: PlatformAdapter,
    private val apiClient: BeaconApiClient,
    private val store: EffectiveConfigStore,
    private val applier: ConfigApplier,
    private val snapshotStore: SnapshotStore?,
    private val fileTreeApplier: FileTreeApplier? = null,
    private val overrideApplier: OverrideSyncApplier? = null,
    // 拓扑变更回调（FR-29）：收到 topology-changed 事件时触发，业务侧据此重查发现端点；为 null 不回调。
    private val topologyListener: (() -> Unit)? = null,
    // 运行指标供给（FR-32）：上报时取当前一帧负载指标（人数 / TPS / 内存 / CPU）；
    // 默认零指标（向后兼容旧行为）；壳层注入平台采集实现以上报真值。
    private val metricsProvider: RuntimeMetricsProvider = { RuntimeMetrics.ZERO },
    // 后端归属供给（FR-36）：注册/上报时取本机（仅 bc 代理）当前代理的后端子服 serverId 集合；
    // 默认空集（bukkit / 旧行为，不上报 backends）；bungee 壳层注入 ProxyServerDirectory 读取。
    private val backendsProvider: () -> List<String> = { emptyList() },
    // BC 专属指标供给（FR-34）：上报时取本机（仅 bc 代理）当前一帧代理负载指标（连接 / 线程 / 运行时长 / 后端可达性·延迟）；
    // 默认 null（bukkit / 旧行为，不上报 proxy 段）；bungee 壳层注入平台采集实现。
    private val proxyMetricsProvider: ProxyMetricsProvider = { null },
    // 反向抓取执行器（FR-39，见 ADR-0027）：收到 SSE command-pending 事件 / READY 对账时触发「拉命令→读 plugins→回传」；
    // 为 null 时不处理命令（向后兼容：未装配执行器的部署不开放反向抓取）。
    private val reverseFetchExecutor: ReverseFetchExecutor? = null,
) {

    private val state = AtomicReference(AgentState.BOOTSTRAP)

    /** 总运行标志：shutdown 后置 false，所有循环据此停转。 */
    private val running = AtomicBoolean(false)

    /** 心跳与长轮询各自独立的「代」标识：重启循环时递增，旧循环自然退出。 */
    private val heartbeatGen = AtomicReference(0)
    private val pollGen = AtomicReference(0)

    /**
     * 周期性指标上报循环（FR-32 / FR-34）的「代」标识：与心跳同周期但独立，重启循环时递增、旧循环自然退出。
     * 此循环把负载指标的上报与「配置是否变更」解耦——稳态配置不变（长轮询恒 304）时仍持续把真值刷进注册表。
     */
    private val metricsReportGen = AtomicReference(0)

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
     * SSE 推送流（FR-24）的「代」标识：注入 streamTransport 时以本流取代上面三条长轮询，重启循环时递增。
     * 单条流合并三通道变更通知 + 连接即对账（见 ADR-0015）。
     */
    private val streamGen = AtomicReference(0)

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

    /** 首次注册成功放行闩：供下游有界等待身份就绪（zone 已回填）后再定身份。 */
    private val firstRegisterLatch = CountDownLatch(1)

    /** 注册成功监听器：供平台壳启动依赖控制面身份的子系统。 */
    private val registeredListeners = CopyOnWriteArrayList<() -> Unit>()

    /** 心跳周期（毫秒）：注册成功前用兜底值，成功后用下发值。 */
    @Volatile
    private var heartbeatIntervalMs: Long = settings.heartbeatFallbackMs

    private val registerBackoff = ExponentialBackoff(settings.backoff)
    private val pollBackoff = ExponentialBackoff(settings.backoff)
    private val fileTreeBackoff = ExponentialBackoff(settings.backoff)
    private val overrideBackoff = ExponentialBackoff(settings.backoff)
    private val streamBackoff = ExponentialBackoff(settings.backoff)

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
     * 立即重同步文件树（运维 resync）：以 fileTreeMd5=null 强制一次清单拉取并由 applier 幂等应用，
     * 旁路文件树长轮询 304，不等超时。
     *
     * 复用 FileTreeApplier 的 fileTreeMd5 幂等守卫：清单未变则只触发一次无害读取、不重复落盘。
     * 独立一发，不接管文件树长轮询主循环、不改其代标识。
     *
     * @return true 表示文件树子系统已启用、已触发同步；false 表示未启用（fileTreeApplier 为 null），未触发。
     */
    fun forceSyncFileTreeNow(): Boolean {
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
        streamGen.set(streamGen.get() + 1)
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
        when (val outcome = apiClient.register(identity, currentBackends())) {
            is RegisterOutcome.Success -> onRegisterSuccess(outcome.result)
            is RegisterOutcome.DuplicateServerId -> {
                adapter.error("注册被拒：重复的 serverId（${identity.serverId}），请检查部署是否冲突", null)
                degradeAndRetryRegister()
            }

            is RegisterOutcome.OfflineRejected -> enterOfflineAndProbe()

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
        // 周期性把负载指标刷进控制面注册表（FR-32 / FR-34）：与配置变更解耦，稳态 304 时仍持续上报真值。
        startMetricsReportLoop()
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
    private fun degradeAndRetryRegister() {
        state.set(AgentState.DEGRADED)
        // 先记下本次注册所属的代，释放单飞门，再安排延迟重试（重试 fire 时按此代校验，过期则作废）。
        val gen = registerGen.get()
        registering.set(false)
        if (!running.get()) return
        val delay = registerBackoff.nextDelayMs()
        adapter.runAsyncDelayed(delay) { beginRegister(gen) }
    }

    /**
     * 被控制面主动下线（FR-49）：进 OFFLINE 态，停止退避猛打，改按大间隔降频探测重注册。
     *
     * 与 DEGRADED（控制面不可用）严格区分：日志只在**首次进入 OFFLINE** 时 WARN 一次、
     * 后续降频探测仍被拒不再打日志，不刷屏；保留快照、不阻断玩家（fail-static 不变）。
     * 取消下线后下一次降频探测即注册成功、回 RUNNING（运维亦可经 reconnect 立即拉起）。
     */
    private fun enterOfflineAndProbe() {
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

    // ---- 周期性指标上报循环（FR-32 / FR-34） ----

    /**
     * 启动周期性指标上报循环：把负载指标（人数 / TPS / 内存 / CPU / BC 专属）的上报与「配置是否变更」解耦。
     *
     * 根因修复：此前 reportApplied 仅在长轮询 / SSE 返回 200（配置 md5 变更）时触发，稳态配置不变恒 304 →
     * 采集供给从不被调用 → 控制面注册表指标恒为零值 → 看板与趋势全 0。现按心跳周期持续上报，让注册表常新。
     * 首跳立即发（注册 / 重连后尽快点亮看板真值），其后每 heartbeatIntervalMs 续杯；全程异步、不阻塞主线程、
     * 采集 / 上报失败均已在内部回退（fail-static 不变）。
     */
    private fun startMetricsReportLoop() {
        val gen = metricsReportGen.get() + 1
        metricsReportGen.set(gen)
        scheduleMetricsReport(gen, 0)
    }

    private fun scheduleMetricsReport(gen: Int, delayMs: Long) {
        if (!running.get()) return
        if (delayMs <= 0) {
            adapter.runAsync { metricsReportTick(gen) }
        } else {
            adapter.runAsyncDelayed(delayMs) { metricsReportTick(gen) }
        }
    }

    private fun metricsReportTick(gen: Int) {
        // 代标识不符（已重启循环或已 shutdown）→ 当前跳作废。
        if (!running.get() || gen != metricsReportGen.get()) return
        // 以本地当前已应用的有效配置 md5 上报（稳态即上次应用值，准确而非陈旧；尚无配置时为空串，控制面按空处理）。
        reportApplied(store.currentMd5() ?: "")
        scheduleMetricsReport(gen, heartbeatIntervalMs)
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
        // 上报失败仅告警，不影响主流程。指标取当前一帧（人数 / TPS / 内存 / CPU），由壳层注入的供给采集；
        // 未注入时为零指标（向后兼容）。本调用在 async 上报线程内，指标采集为廉价 MXBean / Runtime 读取，不阻塞主线程。
        val metrics = currentMetrics()
        val ok = apiClient.report(
            identity,
            appliedMd5,
            playerCount = metrics.playerCount,
            tps = metrics.tps,
            memUsed = metrics.memUsed,
            memMax = metrics.memMax,
            cpuLoad = metrics.cpuLoad,
            backends = currentBackends(),
            proxy = currentProxyMetrics(),
        )
        if (!ok) {
            adapter.warn("上报 applied 状态失败（不影响有效配置生效）")
        }
    }

    /** 取当前一帧运行指标；供给抛异常时回退零指标，绝不让上报因采集失败而中断。 */
    private fun currentMetrics(): RuntimeMetrics {
        return try {
            metricsProvider()
        } catch (e: Exception) {
            adapter.warn("采集运行指标失败，本次按零指标上报：${e.message}")
            RuntimeMetrics.ZERO
        }
    }

    /** 取当前后端归属集合（FR-36）；供给抛异常时回退空集，绝不让注册/上报因采集失败而中断。 */
    private fun currentBackends(): List<String> {
        return try {
            backendsProvider()
        } catch (e: Exception) {
            adapter.warn("采集后端归属集合失败，本次按空集上报：${e.message}")
            emptyList()
        }
    }

    /** 取当前 BC 专属指标（FR-34）；供给抛异常时回退 null（本次不上报 proxy 段），绝不让上报因采集失败而中断。 */
    private fun currentProxyMetrics(): ProxyMetrics? {
        return try {
            proxyMetricsProvider()
        } catch (e: Exception) {
            adapter.warn("采集 BC 专属指标失败，本次不上报 proxy 段：${e.message}")
            null
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

    // ---- SSE 推送流循环（FR-24，注入 streamTransport 时取代上面三条长轮询） ----

    /** 启动单条 SSE 推送流循环：合并三通道变更通知 + 连接即对账（见 ADR-0015）。 */
    private fun startStreamLoop() {
        val gen = streamGen.get() + 1
        streamGen.set(gen)
        scheduleStream(gen, 0)
    }

    private fun scheduleStream(gen: Int, delayMs: Long) {
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
    private fun streamConnect(gen: Int) {
        if (!running.get() || gen != streamGen.get()) return
        // 上报各通道本地当前 md5（空串=本地无该通道内容，控制面补全量）。
        // 拓扑通道 agent 不本地维护摘要，恒上报空串，让控制面在连接即对账时补一次 topology-changed（FR-29）。
        val reported = ReportedChannelMd5(
            config = store.currentMd5() ?: "",
            file = fileTreeApplier?.currentFileTreeMd5() ?: "",
            override = overrideApplier?.currentOverrideMd5() ?: "",
            topology = "",
        )
        apiClient.openStream(identity, reported, StreamLoopListener(gen))
    }

    /**
     * SSE 事件分发：按事件类型触发对应通道的强制重取-应用（复用现有 HTTP 端点逻辑，见 ADR-0015 决策 2）。
     *
     * 事件 data 行携带的 md5 仅作"有变更"通知、agent 不消费它——*-changed 一律忽略 event.data，
     * 改用本地已应用的 md5 走现有端点重拉（端点比对 md5，真变才返 200），故此处不解析载荷。
     */
    private fun dispatchStreamEvent(gen: Int, event: StreamEvent) {
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
    private fun triggerReverseFetch() {
        val executor = reverseFetchExecutor ?: return
        adapter.runAsync { executor.trigger() }
    }

    /** 流结束处理：进 DEGRADED（连接级降级）、保留本地快照，退避后重连（重连即再次对账）。 */
    private fun onStreamClosed(gen: Int, error: Throwable?) {
        if (!running.get() || gen != streamGen.get()) return
        state.set(AgentState.DEGRADED)
        val delay = streamBackoff.nextDelayMs()
        val reason = error?.message ?: "正常关闭"
        adapter.warn("SSE 推送流断开（$reason），保持当前有效配置，${delay}ms 后重连并对账")
        scheduleStream(gen, delay)
    }

    /**
     * config-changed：以当前本地 md5 拉一次 config/effective（服务端 md5 已变 → 立即 200，不挂起），apply 并 report。
     * 用 ConfigApplier 的 md5 幂等守卫兜底重复事件；404 触发重新注册。
     */
    private fun fetchAndApplyConfigOnce() {
        when (val result = apiClient.pollEffective(identity, store.currentMd5(), settings.requestTimeoutMs)) {
            is PollResult.Changed -> {
                applier.apply(result.effective)
                reportApplied(result.effective.md5)
                markRunningAfterStreamSuccess()
            }

            is PollResult.NotModified -> markRunningAfterStreamSuccess() // 已是最新（重复事件），无害
            is PollResult.NotRegistered -> {
                adapter.warn("SSE 取配置返回未注册，触发重新注册")
                triggerReregister()
            }

            is PollResult.Failed -> adapter.warn("SSE 取配置失败（${result.reason}），保持当前有效配置，待下次事件/重连")
        }
    }

    /** file-changed：以当前本地 fileTreeMd5 拉一次 files/manifest 并增量同步落盘（fail-static 由 applier 内部把控）。 */
    private fun fetchAndApplyFileTreeOnce() {
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

    /** override-changed：以当前本地 overrideMd5 拉一次 override-sets 并落盘（fail-static 由 applier 内部把控）。 */
    private fun fetchAndApplyOverrideOnce() {
        val applierLocal = overrideApplier ?: return
        when (val result = apiClient.pollOverrideSets(identity, applierLocal.currentOverrideMd5(), settings.requestTimeoutMs)) {
            is OverridePollResult.Changed -> {
                applierLocal.apply(result.manifest)
                markRunningAfterStreamSuccess()
            }

            is OverridePollResult.NotModified -> markRunningAfterStreamSuccess()
            is OverridePollResult.NotRegistered -> {
                adapter.warn("SSE 取覆盖集返回未注册，触发重新注册")
                triggerReregister()
            }

            is OverridePollResult.Failed -> adapter.warn("SSE 取覆盖集失败（${result.reason}），保留本地覆盖不动，待下次事件/重连")
        }
    }

    /**
     * topology-changed（FR-29）：控制面只发"拓扑变了"通知、不搬实例数据，故此处不取数据，
     * 仅回调拓扑监听器，由业务侧自行重查发现端点取最新拓扑。
     */
    private fun fireTopologyChanged() {
        val listener = topologyListener ?: return
        try {
            listener()
        } catch (e: Exception) {
            adapter.warn("拓扑监听器回调执行失败：${e.message}")
        }
    }

    /** SSE 取数据成功一轮：重置流退避并从 DEGRADED 恢复 RUNNING（健康判活仍由心跳决定，与此解耦）。 */
    private fun markRunningAfterStreamSuccess() {
        streamBackoff.reset()
        if (state.get() == AgentState.DEGRADED) {
            state.set(AgentState.RUNNING)
            adapter.info("SSE 推送流已恢复，回到 RUNNING")
        }
    }

    /** SSE 流监听器：把 transport 回调桥接到生命周期的事件分发与重连，携带流代标识自我作废过期回调。 */
    private inner class StreamLoopListener(private val gen: Int) : StreamListener {

        override fun onOpen() {
            if (!running.get() || gen != streamGen.get()) return
            streamBackoff.reset()
            adapter.info("SSE 推送流已建立，开始连接即对账")
        }

        override fun onEvent(event: StreamEvent) {
            // 事件处理放异步线程，不阻塞 transport 的读流线程（取数据-应用本身可能含 IO）。
            adapter.runAsync { dispatchStreamEvent(gen, event) }
        }

        override fun onClosed(error: Throwable?) {
            onStreamClosed(gen, error)
        }
    }
}
