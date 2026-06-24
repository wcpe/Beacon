package top.wcpe.beacon.agent.core

import top.wcpe.beacon.agent.api.BeaconAgent
import top.wcpe.beacon.agent.core.api.BeaconAgentImpl
import top.wcpe.beacon.agent.core.api.DiscoveryView
import top.wcpe.beacon.agent.core.api.EffectiveConfigView
import top.wcpe.beacon.agent.core.api.TopologyWatchHub
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.command.ReverseFetchExecutor
import top.wcpe.beacon.agent.core.config.ConfigApplier
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.filetree.AppliedFileManifestStore
import top.wcpe.beacon.agent.core.filetree.FileMirrorWriter
import top.wcpe.beacon.agent.core.filetree.FileTreeApplier
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.lifecycle.AgentLifecycle
import top.wcpe.beacon.agent.core.log.AgentLogBuffer
import top.wcpe.beacon.agent.core.log.BufferingPlatformAdapter
import top.wcpe.beacon.agent.core.messaging.MessagingHolder
import top.wcpe.beacon.agent.core.messaging.RosterDirectoryHolder
import top.wcpe.beacon.agent.core.metrics.ProxyMetricsProvider
import top.wcpe.beacon.agent.core.metrics.RuntimeMetrics
import top.wcpe.beacon.agent.core.metrics.RuntimeMetricsProvider
import top.wcpe.beacon.agent.core.override.CommandWhitelist
import top.wcpe.beacon.agent.core.override.OverrideSyncApplier
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.snapshot.SnapshotStore
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import top.wcpe.beacon.agent.core.transport.StreamTransport
import java.io.File
import java.util.concurrent.atomic.AtomicReference

/**
 * 装配产物：把 lifecycle 与对外门面交回壳层。
 */
class AssembledAgent(
    val lifecycle: AgentLifecycle,
    val beaconAgent: BeaconAgent,
    val apiClient: BeaconApiClient,
    // 跨服消息门面持有者（FR-26）：默认 DisabledMessaging；壳层在消息模块启动成功后 set 活跃门面。
    val messagingHolder: MessagingHolder,
    // 玩家位置名册只读端口持有者（FR-31）：默认空名册；壳层在消息模块启动成功后 set 活跃实现、停止时 reset。
    val rosterDirectoryHolder: RosterDirectoryHolder,
)

/**
 * core 侧统一装配：用 transport/codec/adapter/settings/identity 组装出 lifecycle + 门面，
 * 两个平台壳共用，杜绝重复装配代码。
 *
 * 注意：EffectiveConfigView 在装配时创建并经 store 暴露，壳层需让自己的 PlatformAdapter
 * 在 publishConfigChanged 时调用 view.fireChanged（派发 API 监听器）。为此本装配返回前
 * 不持有 adapter→view 的引用，由壳层在创建 adapter 时注入 view（见各壳）。
 */
object AgentAssembly {

    fun assemble(
        identity: AgentIdentity,
        settings: AgentSettings,
        rawAdapter: PlatformAdapter,
        transport: HttpTransport,
        codec: JsonCodec,
        store: EffectiveConfigStore,
        effectiveConfigView: EffectiveConfigView,
        // 流式传输（SSE 推送，FR-24）：壳层注入 OkHttpStreamTransport 即启用单条流取代三条长轮询；
        // 为 null 时退回长轮询（迁移期兼容，见 ADR-0015 决策 8）。
        streamTransport: StreamTransport? = null,
        // 运行指标供给（FR-32）：壳层注入平台采集实现（人数 / TPS + JVM 内存 / CPU）以上报真值；
        // 默认零指标（未注入时向后兼容旧行为）。
        metricsProvider: RuntimeMetricsProvider = { RuntimeMetrics.ZERO },
        // 后端归属供给（FR-36）：bungee 壳层注入 ProxyServerDirectory 读取「当前代理的后端 serverId 集合」；
        // 默认空集（bukkit / 未注入时不上报 backends，向后兼容）。
        backendsProvider: () -> List<String> = { emptyList() },
        // BC 专属指标供给（FR-34）：bungee 壳层注入平台采集实现（连接 / 线程 / 运行时长 / 后端可达性·延迟）；
        // 默认 null（bukkit / 未注入时不上报 proxy 段，向后兼容）。
        proxyMetricsProvider: ProxyMetricsProvider = { null },
        // agent 自身 dataFolder 顶段名集合（如 `BeaconAgent` / `BeaconAgentProxy`）：壳层注入自身 plugin 名，
        // 文件树 applier 据此跳过命中顶段的 path，防止运维误把 agent 自管文件经 FR-14/FR-38 塞进有效树后污染自身。
        // 默认空集（未注入时回到旧语义，向后兼容），core 不硬编码任何 plugin 名（守 ADR-0005）。
        selfPluginDirNames: Set<String> = emptySet(),
    ): AssembledAgent {
        // agent 自身日志环形缓冲（FR-88，见 ADR-0040）：包裹壳层 adapter，使所有经 core 的日志旁路进缓冲（落缓冲即脱敏），
        // 供 tail-logs 命令读快照回传。绝不读任何磁盘日志文件。壳层日志实现零改动。
        val logBuffer = AgentLogBuffer(capacity = LOG_BUFFER_CAPACITY)
        val adapter: PlatformAdapter = BufferingPlatformAdapter(rawAdapter, logBuffer)

        val apiClient = BeaconApiClient(transport, codec, settings, streamTransport)

        val snapshotStore: SnapshotStore? = if (settings.snapshotEnabled) {
            SnapshotStore(File(adapter.dataFolder(), settings.snapshotFileName), codec)
        } else {
            null
        }

        val applier = ConfigApplier(store, snapshotStore, adapter)

        // 镜像落盘根 = plugins 基目录（FR-14 文件树 / FR-15 覆盖落盘与 ADR-0011 路径限定共用）。
        // fail-closed 守卫：若解析出的基目录名不是 "plugins"（getDataFolder 异常 / agent 未装在 plugins/<自身> 下），
        // 关闭文件树与三方覆盖落盘——宁可不落，也不把文件落到错误目录（该类路径解析意外正是本次 E2E 暴露的缺陷根源）。
        val pluginsBase = adapter.pluginsBaseFolder()
        val pluginsBaseValid = pluginsBase.name.equals("plugins", ignoreCase = true)
        if (settings.fileTree.enabled && !pluginsBaseValid) {
            adapter.error(
                "plugins 基目录解析异常（期望目录名为 plugins，实得 '${pluginsBase.name}'，路径=${pluginsBase.absolutePath}）：" +
                    "fail-closed 关闭文件树与三方覆盖落盘，避免落到错误目录",
                null,
            )
        }
        val mirrorEnabled = settings.fileTree.enabled && pluginsBaseValid

        // 文件树托管（通道B）：启用且基目录有效时装配镜像落盘 + 已落盘清单 + 编排器（取内容委托 apiClient）。
        val fileTreeApplier: FileTreeApplier? = if (mirrorEnabled) {
            val root = if (settings.fileTree.targetSubDir.isBlank()) {
                pluginsBase
            } else {
                File(pluginsBase, settings.fileTree.targetSubDir)
            }
            FileTreeApplier(
                mirrorWriter = FileMirrorWriter(root),
                appliedStore = AppliedFileManifestStore(
                    File(adapter.dataFolder(), settings.fileTree.appliedManifestFileName),
                    codec,
                ),
                adapter = adapter,
                fetchContent = { path -> apiClient.fetchFileContent(identity, path) },
                protectedSegments = selfPluginDirNames,
            )
        } else {
            null
        }

        // 三方覆盖集接线（FR-15）：仅在文件树启用且基目录有效时装配（覆盖集是通道B 的一个 profile，依赖镜像落盘能力）。
        // 命令白名单本地配置、默认空（控制面不下发；空即命令派发能力关闭，见 ADR-0011 决策 3）。
        val overrideApplier: OverrideSyncApplier? = if (mirrorEnabled) {
            OverrideSyncApplier(
                pluginsBaseFolder = pluginsBase,
                backupRoot = File(adapter.dataFolder(), settings.override.backupDirName),
                whitelist = CommandWhitelist(settings.override.commandWhitelist),
                adapter = adapter,
                fetchMember = { setName, path -> apiClient.fetchOverrideMember(identity, setName, path) },
            )
        } else {
            null
        }

        // 强制重同步回调（FR-91）的延迟持有者：executor 先于 lifecycle 构造，回调命令期才触发，
        // 故用可变引用打破构造顺序——lifecycle 建好后回填，命令到达时再解引用调用。
        val lifecycleRef = AtomicReference<AgentLifecycle?>(null)

        // 反向抓取执行器（FR-39，见 ADR-0027）：仅在 plugins 基目录有效时装配（与文件树同一 fail-closed 守卫，
        // 避免从错误目录读盘上传）。读盘委托 adapter.readPluginsTree（壳层实现 FS 级路径安全）。
        // 取日志（FR-88）/ 强制重同步（FR-91）不依赖 plugins 基目录有效性（不读盘）；故执行器始终装配以响应这两类命令。
        // 反向抓取（读盘）仍受 pluginsBaseValid fail-closed 守卫：基目录无效时禁读盘上传（避免从错误目录读），
        // 由 reverseFetchEnabled 关闭该路径——tail-logs / resync-config 不受影响。三类命令复用同一命令通路与单飞排空。
        val reverseFetchExecutor = ReverseFetchExecutor(
            identity, apiClient, adapter, logBuffer,
            onResyncConfig = { lifecycleRef.get()?.forceResyncNow() },
            reverseFetchEnabled = pluginsBaseValid,
        )

        // 拓扑 watch 监听器表（FR-29）：DiscoveryView.watch 注册、AgentLifecycle 收到 topology-changed 事件后扇出。
        val topologyWatchHub = TopologyWatchHub()

        val lifecycle = AgentLifecycle(
            identity = identity,
            settings = settings,
            adapter = adapter,
            apiClient = apiClient,
            store = store,
            applier = applier,
            snapshotStore = snapshotStore,
            fileTreeApplier = fileTreeApplier,
            overrideApplier = overrideApplier,
            // 拓扑变更事件 → 扇出到 watch 监听器（业务侧据此重查发现端点）。
            topologyListener = { topologyWatchHub.fireTopologyChanged() },
            // 运行指标供给（FR-32）：上报时取当前一帧负载指标。
            metricsProvider = metricsProvider,
            // 后端归属供给（FR-36）：注册/上报时取当前代理的后端 serverId 集合。
            backendsProvider = backendsProvider,
            // BC 专属指标供给（FR-34）：上报时取当前一帧代理负载指标（仅 bc 注入）。
            proxyMetricsProvider = proxyMetricsProvider,
            // 反向抓取执行器（FR-39）：收到 SSE command-pending / READY 时触发拉命令→读 plugins→回传。
            reverseFetchExecutor = reverseFetchExecutor,
        )
        // 回填强制重同步回调持有者（FR-91）：lifecycle 已建好，命令期 onResyncConfig 经此解引用调用 forceResyncNow。
        lifecycleRef.set(lifecycle)

        // 玩家位置名册只读端口持有者（FR-31）：装配期即建（早于消息模块启动），默认空名册降级；
        // 壳层在消息模块就绪后注入 Redis 实现。
        val rosterDirectoryHolder = RosterDirectoryHolder(warn = adapter::warn)
        val discoveryView = DiscoveryView(apiClient, topologyWatchHub, rosterDirectoryHolder)
        // 跨服消息门面持有者（FR-26）：默认 DisabledMessaging，壳层在消息模块就绪后注入活跃门面。
        val messagingHolder = MessagingHolder()
        val beaconAgent = BeaconAgentImpl(identity, store, lifecycle, effectiveConfigView, discoveryView, messagingHolder)

        return AssembledAgent(lifecycle, beaconAgent, apiClient, messagingHolder, rosterDirectoryHolder)
    }

    /** agent 自身日志环形缓冲容量（FR-88，见 ADR-0040）：最近 N 行，够排障、内存可忽略；有界不溢出。 */
    private const val LOG_BUFFER_CAPACITY = 300
}
