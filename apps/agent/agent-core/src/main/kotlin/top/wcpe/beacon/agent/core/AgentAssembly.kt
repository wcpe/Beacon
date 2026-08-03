package top.wcpe.beacon.agent.core

import top.wcpe.beacon.agent.api.BeaconAgent
import top.wcpe.beacon.agent.core.api.BeaconAgentImpl
import top.wcpe.beacon.agent.core.api.DiscoveryView
import top.wcpe.beacon.agent.core.api.EffectiveConfigView
import top.wcpe.beacon.agent.core.api.TopologyWatchHub
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.fetchFileContent
import top.wcpe.beacon.agent.core.client.fetchOverrideMember
import top.wcpe.beacon.agent.core.command.ReverseFetchExecutor
import top.wcpe.beacon.agent.core.config.ConfigApplier
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.delivery.DeliveryBackupManager
import top.wcpe.beacon.agent.core.delivery.DeliveryCommandExecutor
import top.wcpe.beacon.agent.core.delivery.DeliveryDownloader
import top.wcpe.beacon.agent.core.delivery.DeliveryOverwriter
import top.wcpe.beacon.agent.core.delivery.DeliveryPipeline
import top.wcpe.beacon.agent.core.delivery.DeliveryTargetResolver
import top.wcpe.beacon.agent.core.delivery.DeliveryUploader
import top.wcpe.beacon.agent.core.filetree.AppliedFileManifestStore
import top.wcpe.beacon.agent.core.filetree.AssetManifestStore
import top.wcpe.beacon.agent.core.filetree.FileMirrorWriter
import top.wcpe.beacon.agent.core.filetree.FileTreeApplier
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.lifecycle.AgentLifecycle
import top.wcpe.beacon.agent.core.lifecycle.AgentLifecycleHooks
import top.wcpe.beacon.agent.core.lifecycle.AssetScanCoordinator
import top.wcpe.beacon.agent.core.lifecycle.AssetScanScope
import top.wcpe.beacon.agent.core.lifecycle.forceResyncNow
import top.wcpe.beacon.agent.core.log.AgentLogBuffer
import top.wcpe.beacon.agent.core.log.BufferingPlatformAdapter
import top.wcpe.beacon.agent.core.messaging.HttpMessageTransport
import top.wcpe.beacon.agent.core.messaging.MessageBus
import top.wcpe.beacon.agent.core.messaging.MessagePollCoordinator
import top.wcpe.beacon.agent.core.messaging.MessagingHolder
import top.wcpe.beacon.agent.core.messaging.MessagingRuntime
import top.wcpe.beacon.agent.core.messaging.RosterDirectoryHolder
import top.wcpe.beacon.agent.core.metrics.ProxyMetricsProvider
import top.wcpe.beacon.agent.core.metrics.RuntimeMetrics
import top.wcpe.beacon.agent.core.metrics.RuntimeMetricsProvider
import top.wcpe.beacon.agent.core.override.CommandWhitelist
import top.wcpe.beacon.agent.core.override.OverrideSyncApplier
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.scheduling.CandidateSnapshot
import top.wcpe.beacon.agent.core.scheduling.LocalDecisionReportQueue
import top.wcpe.beacon.agent.core.scheduling.SchedulingCache
import top.wcpe.beacon.agent.core.scheduling.SchedulingRefresher
import top.wcpe.beacon.agent.core.scheduling.SchedulingSnapshotStore
import top.wcpe.beacon.agent.core.scheduling.SchedulingView
import top.wcpe.beacon.agent.core.scheduling.SelfHealthHolder
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.snapshot.SnapshotStore
import top.wcpe.beacon.agent.core.transport.BlobStreamTransport
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import top.wcpe.beacon.agent.core.transport.StreamTransport
import java.io.File
import java.util.concurrent.atomic.AtomicReference

/** 传输层配置：HTTP 传输 + JSON 编解码 + 可选 SSE / blob 流。 */
data class TransportConfig(
    val transport: HttpTransport,
    val codec: JsonCodec,
    // SSE 推送流（FR-24）：为 null 退回长轮询。
    val streamTransport: StreamTransport? = null,
    // 交付 blob 流（FR-165）：为 null 不响应 delivery_* 命令。
    val blobStreamTransport: BlobStreamTransport? = null,
)

/** 装配可选钩子：指标供给 / 后端归属 / 自身插件名 / BC 目录重同步 / 权威身份失效回调。 */
data class AssemblyHooks(
    val metricsProvider: RuntimeMetricsProvider = { RuntimeMetrics.ZERO },
    val backendsProvider: () -> List<String> = { emptyList() },
    val proxyMetricsProvider: ProxyMetricsProvider = { null },
    val selfPluginDirNames: Set<String> = emptySet(),
    val onBcDirectoryResync: (() -> Boolean)? = null,
    val authorityInvalidated: () -> Unit = {},
)

/** 配置上下文：有效配置存储 + 只读视图（壳层在创建 adapter 时注入 view 回调）。 */
data class ConfigContext(
    val store: EffectiveConfigStore,
    val effectiveConfigView: EffectiveConfigView,
)

/** 跨服消息装配产物：门面持有者 + 名册持有者 + 运行时。 */
data class MessagingAssembly(
    val messagingHolder: MessagingHolder,
    val rosterDirectoryHolder: RosterDirectoryHolder,
    val messagingRuntime: MessagingRuntime,
)

/**
 * 装配产物：把 lifecycle 与对外门面交回壳层。
 */
class AssembledAgent(
    val lifecycle: AgentLifecycle,
    val beaconAgent: BeaconAgent,
    val apiClient: BeaconApiClient,
    // 跨服消息装配产物（门面持有者 + 名册持有者 + 运行时）。
    val messaging: MessagingAssembly,
    /** 供 BC 壳层只读大厅候选快照；不得通过此引用修改调度缓存。 */
    val lobbySnapshotProvider: () -> CandidateSnapshot?,
)

// core 侧统一装配：用 transport/codec/adapter/settings/identity 组装出 lifecycle + 门面，
// 两个平台壳共用，杜绝重复装配代码。
//
// 注意：EffectiveConfigView 在装配时创建并经 store 暴露，壳层需让自己的 PlatformAdapter
// 在 publishConfigChanged 时调用 view.fireChanged（派发 API 监听器）。为此本装配返回前
// 不持有 adapter→view 的引用，由壳层在创建 adapter 时注入 view（见各壳）。

/** 装配上下文：跨多个子装配函数共享的依赖集合。 */
private data class AssemblyContext(
    val adapter: PlatformAdapter,
    val settings: AgentSettings,
    val apiClient: BeaconApiClient,
    val identity: AgentIdentity,
    val codec: JsonCodec,
    val logBuffer: AgentLogBuffer,
)

/** 镜像落盘上下文：plugins 基目录 + 有效性 + 镜像启用标志。 */
private data class MirrorContext(
    val pluginsBase: File,
    val pluginsBaseValid: Boolean,
    val mirrorEnabled: Boolean,
)

/** 核心装配参数：配置存储 + 快照存储 + 配置应用器。 */
private data class CoreAssemblyParams(
    val store: EffectiveConfigStore,
    val snapshotStore: SnapshotStore?,
    val applier: ConfigApplier,
)

/** 装配引用：钩子 + 可变引用（需跨函数传递的 AtomicReference）。 */
private data class AssemblyRefs(
    val hooks: AssemblyHooks,
    val schedulingRefresherRef: AtomicReference<SchedulingRefresher?>,
)

/** 生命周期与执行器装配产物。 */
private data class LifecycleAndExecutor(
    val lifecycle: AgentLifecycle,
    val topologyWatchHub: TopologyWatchHub,
    val scheduling: SchedulingComponents,
    val messaging: MessagingComponents,
)

/** 调度组件装配产物。 */
private data class SchedulingComponents(
    val selfHealthHolder: SelfHealthHolder,
    val schedulingCache: SchedulingCache,
    val schedulingView: SchedulingView,
    val schedulingRefresher: SchedulingRefresher,
)

/** 跨服消息组件装配产物。 */
private data class MessagingComponents(
    val messagingHolder: MessagingHolder,
    val messageBus: MessageBus,
    val messagingRuntime: MessagingRuntime,
)

object AgentAssembly {
    fun assemble(
        identity: AgentIdentity,
        settings: AgentSettings,
        rawAdapter: PlatformAdapter,
        transport: TransportConfig,
        config: ConfigContext,
        hooks: AssemblyHooks = AssemblyHooks(),
    ): AssembledAgent {
        val store = config.store
        val effectiveConfigView = config.effectiveConfigView
        // agent 自身日志环形缓冲（FR-88，见 ADR-0040）：包裹壳层 adapter，使所有经 core 的日志旁路进缓冲（落缓冲即脱敏），
        // 供 tail-logs 命令读快照回传。绝不读任何磁盘日志文件。壳层日志实现零改动。
        val logBuffer = AgentLogBuffer(capacity = LOG_BUFFER_CAPACITY)
        val adapter: PlatformAdapter = BufferingPlatformAdapter(rawAdapter, logBuffer)

        val apiClient = BeaconApiClient(transport.transport, transport.codec, settings, transport.streamTransport)

        val snapshotStore: SnapshotStore? =
            if (settings.snapshotEnabled) {
                SnapshotStore(File(adapter.dataFolder(), settings.snapshotFileName), transport.codec)
            } else {
                null
            }

        val applier = ConfigApplier(store, snapshotStore, adapter)

        // 装配上下文：跨多个子装配函数共享的依赖集合。
        val ctx = AssemblyContext(adapter, settings, apiClient, identity, transport.codec, logBuffer)

        // 镜像落盘根 = plugins 基目录（FR-14 文件树 / FR-15 覆盖落盘与 ADR-0011 路径限定共用）。
        // fail-closed 守卫：若解析出的基目录名不是 "plugins"，关闭文件树与三方覆盖落盘。
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
        val schedulingRefresherRef = AtomicReference<SchedulingRefresher?>(null)

        // 装配 lifecycle + 反向抓取执行器 + 调度 + 消息（集中在一个子函数内，保持 assemble 简洁）。
        val le =
            createLifecycleAndExecutor(
                ctx,
                transport,
                CoreAssemblyParams(store, snapshotStore, applier),
                MirrorContext(pluginsBase, pluginsBaseValid, mirrorEnabled),
                AssemblyRefs(hooks, schedulingRefresherRef),
            )
        val lifecycle = le.lifecycle
        val schedulingCache = le.scheduling.schedulingCache
        val schedulingView = le.scheduling.schedulingView
        val messagingHolder = le.messaging.messagingHolder
        val messagingRuntime = le.messaging.messagingRuntime

        // 玩家位置名册只读端口持有者（FR-31）：装配期即建（早于消息模块启动），默认空名册降级；
        // 壳层在消息模块就绪后注入 Redis 实现。
        val rosterDirectoryHolder = RosterDirectoryHolder(warn = adapter::warn)
        val discoveryView = DiscoveryView(apiClient, le.topologyWatchHub, rosterDirectoryHolder, identity)
        val beaconAgent =
            BeaconAgentImpl(identity, store, lifecycle, effectiveConfigView, discoveryView, messagingHolder, schedulingView)

        return AssembledAgent(
            lifecycle,
            beaconAgent,
            apiClient,
            MessagingAssembly(messagingHolder, rosterDirectoryHolder, messagingRuntime),
            schedulingCache::current,
        )
    }

    /** 装配反向抓取执行器（FR-39）与生命周期（FR-148），返回 lifecycle + topologyWatchHub。 */
    private fun createLifecycleAndExecutor(
        ctx: AssemblyContext,
        transport: TransportConfig,
        core: CoreAssemblyParams,
        mirror: MirrorContext,
        refs: AssemblyRefs,
    ): LifecycleAndExecutor {
        val lifecycleRef = AtomicReference<AgentLifecycle?>(null)
        val fileTreeApplier = createFileTreeApplier(ctx, mirror.mirrorEnabled, mirror.pluginsBase, refs.hooks.selfPluginDirNames)
        val overrideApplier = createOverrideApplier(ctx, mirror.mirrorEnabled, mirror.pluginsBase)
        val assetScanCoordinator = createAssetScanCoordinator(ctx, mirror.pluginsBaseValid, mirror.pluginsBase)
        val deliveryExecutor = createDeliveryExecutor(ctx, transport, mirror.pluginsBaseValid, mirror.pluginsBase)
        val reverseFetchExecutor =
            ReverseFetchExecutor(
                ctx.identity,
                ctx.apiClient,
                ctx.adapter,
                ctx.logBuffer,
                onResyncConfig = { lifecycleRef.get()?.forceResyncNow() ?: false },
                reverseFetchEnabled = mirror.pluginsBaseValid,
                onAssetRescan = { force -> assetScanCoordinator?.forceScanNow(force) ?: false },
                deliveryExecutor = deliveryExecutor,
                onBcDirectoryResync =
                    refs.hooks.onBcDirectoryResync?.let { syncDirectory ->
                        { refs.schedulingRefresherRef.get()?.refreshNow() == true && syncDirectory() }
                    },
            )
        val topologyWatchHub = TopologyWatchHub()
        val scheduling = createSchedulingComponents(ctx, refs.schedulingRefresherRef)
        val messaging = createMessagingComponents(ctx)
        val lifecycle =
            AgentLifecycle(
                identity = ctx.identity,
                settings = ctx.settings,
                adapter = ctx.adapter,
                apiClient = ctx.apiClient,
                store = core.store,
                applier = core.applier,
                snapshotStore = core.snapshotStore,
                hooks =
                    AgentLifecycleHooks(
                        fileTreeApplier = fileTreeApplier,
                        overrideApplier = overrideApplier,
                        topologyListener = { topologyWatchHub.fireTopologyChanged() },
                        metricsProvider = refs.hooks.metricsProvider,
                        backendsProvider = refs.hooks.backendsProvider,
                        proxyMetricsProvider = refs.hooks.proxyMetricsProvider,
                        reverseFetchExecutor = reverseFetchExecutor,
                        schedulingRuntime = scheduling.schedulingRefresher,
                        selfHealthSink = scheduling.selfHealthHolder::set,
                        assetScan = assetScanCoordinator,
                        authorityInvalidated = refs.hooks.authorityInvalidated,
                    ),
            )
        lifecycle.onRegistered { messaging.messagingRuntime.start() }
        lifecycleRef.set(lifecycle)
        return LifecycleAndExecutor(lifecycle, topologyWatchHub, scheduling, messaging)
    }

    private fun createFileTreeApplier(
        ctx: AssemblyContext,
        mirrorEnabled: Boolean,
        pluginsBase: File,
        protectedSegments: Set<String>,
    ): FileTreeApplier? =
        if (mirrorEnabled) {
            val root =
                if (ctx.settings.fileTree.targetSubDir.isBlank()) {
                    pluginsBase
                } else {
                    File(pluginsBase, ctx.settings.fileTree.targetSubDir)
                }
            FileTreeApplier(
                mirrorWriter = FileMirrorWriter(root),
                appliedStore =
                    AppliedFileManifestStore(
                        File(ctx.adapter.dataFolder(), ctx.settings.fileTree.appliedManifestFileName),
                        ctx.codec,
                    ),
                adapter = ctx.adapter,
                fetchContent = { path -> ctx.apiClient.fetchFileContent(ctx.identity, path) },
                protectedSegments = protectedSegments,
            )
        } else {
            null
        }

    /** 装配三方覆盖集对账器（FR-15）：仅在镜像启用时装配。 */
    private fun createOverrideApplier(
        ctx: AssemblyContext,
        mirrorEnabled: Boolean,
        pluginsBase: File,
    ): OverrideSyncApplier? =
        if (mirrorEnabled) {
            OverrideSyncApplier(
                pluginsBaseFolder = pluginsBase,
                backupRoot = File(ctx.adapter.dataFolder(), ctx.settings.override.backupDirName),
                whitelist = CommandWhitelist(ctx.settings.override.commandWhitelist),
                adapter = ctx.adapter,
                fetchMember = { setName, path -> ctx.apiClient.fetchOverrideMember(ctx.identity, setName, path) },
            )
        } else {
            null
        }

    /** 装配文件资产索引周期扫描协调器（FR-163）：启用且基目录有效时装配。 */
    private fun createAssetScanCoordinator(
        ctx: AssemblyContext,
        pluginsBaseValid: Boolean,
        pluginsBase: File,
    ): AssetScanCoordinator? =
        if (ctx.settings.assets.enabled && pluginsBaseValid) {
            AssetScanCoordinator(
                adapter = ctx.adapter,
                apiClient = ctx.apiClient,
                identity = ctx.identity,
                store = AssetManifestStore(File(ctx.adapter.dataFolder(), ctx.settings.assets.manifestFileName), ctx.codec),
                scope = AssetScanScope(serverRoot = { pluginsBase.parentFile ?: pluginsBase }, selfDataDir = ctx.adapter.dataFolder()),
                intervalMs = ctx.settings.assets.scanIntervalSec * 1000L,
            )
        } else {
            null
        }

    /** 装配交付命令执行器（FR-165）：注入了 blob 流式传输且基目录有效时装配。 */
    private fun createDeliveryExecutor(
        ctx: AssemblyContext,
        transport: TransportConfig,
        pluginsBaseValid: Boolean,
        pluginsBase: File,
    ): DeliveryCommandExecutor? =
        if (transport.blobStreamTransport != null && pluginsBaseValid) {
            val serverRoot = pluginsBase.parentFile ?: pluginsBase
            val resolver = DeliveryTargetResolver(serverRoot, ctx.adapter.dataFolder())
            val blobUrl: (String) -> String = { sha -> "${ctx.settings.primaryEndpoint()}/beacon/v2/stream/delivery/blobs/$sha" }
            val authHeaders: () -> Map<String, String> = { ctx.apiClient.agentAuthHeaders(ctx.identity) }
            val pipeline =
                DeliveryPipeline(
                    uploader = DeliveryUploader(transport.blobStreamTransport, resolver, blobUrl, authHeaders, ctx.adapter),
                    downloader = DeliveryDownloader(transport.blobStreamTransport, blobUrl, authHeaders, ctx.adapter),
                    backupManager =
                        DeliveryBackupManager(
                            File(ctx.adapter.dataFolder(), DELIVERY_BACKUPS_DIR),
                            resolver,
                            transport.codec,
                            ctx.adapter,
                        ),
                    overwriter = DeliveryOverwriter(resolver),
                    tempRoot = File(ctx.adapter.dataFolder(), DELIVERY_TMP_DIR),
                )
            DeliveryCommandExecutor(ctx.identity, ctx.apiClient, ctx.adapter, pipeline)
        } else {
            null
        }

    /** 装配调度组件（FR-148）：自身健康持有者、候选缓存、补报队列、落盘、门面视图、刷新循环。 */
    private fun createSchedulingComponents(
        ctx: AssemblyContext,
        schedulingRefresherRef: AtomicReference<SchedulingRefresher?>,
    ): SchedulingComponents {
        val selfHealthHolder = SelfHealthHolder()
        val schedulingCache = SchedulingCache()
        val reportQueue = LocalDecisionReportQueue()
        val schedulingSnapshotStore: SchedulingSnapshotStore? =
            if (ctx.settings.snapshotEnabled) {
                SchedulingSnapshotStore(File(ctx.adapter.dataFolder(), CANDIDATES_SNAPSHOT_FILE), ctx.codec)
            } else {
                null
            }
        val schedulingView = SchedulingView(ctx.apiClient, ctx.identity, ctx.adapter, schedulingCache, reportQueue, selfHealthHolder)
        val schedulingRefresher =
            SchedulingRefresher(ctx.apiClient, ctx.identity, ctx.adapter, schedulingCache, schedulingSnapshotStore, reportQueue)
        schedulingRefresherRef.set(schedulingRefresher)
        return SchedulingComponents(selfHealthHolder, schedulingCache, schedulingView, schedulingRefresher)
    }

    /** 装配跨服消息组件（FR-149，HTTP 中转）：holder、bus、runtime。 */
    private fun createMessagingComponents(ctx: AssemblyContext): MessagingComponents {
        val messagingHolder = MessagingHolder()
        val messageBus =
            MessageBus(
                transport = HttpMessageTransport(ctx.apiClient, ctx.identity, ctx.codec, warn = ctx.adapter::warn),
                codec = ctx.codec,
                selfServerId = ctx.identity.serverId,
                settings = ctx.settings.messaging,
                playerLocator = null,
                scheduleTimeout = ctx.adapter::runAsyncDelayed,
                outboundExecutor = ctx.adapter::runAsync,
                warn = ctx.adapter::warn,
            )
        val messagingRuntime =
            MessagingRuntime(
                settings = ctx.settings.messaging,
                holder = messagingHolder,
                bus = messageBus,
                poll = MessagePollCoordinator(ctx.apiClient, ctx.identity, ctx.adapter, messageBus),
                info = ctx.adapter::info,
                error = ctx.adapter::error,
            )
        return MessagingComponents(messagingHolder, messageBus, messagingRuntime)
    }

    /** agent 自身日志环形缓冲容量（FR-88，见 ADR-0040）：最近 N 行，够排障、内存可忽略；有界不溢出。 */
    private const val LOG_BUFFER_CAPACITY = 300

    /** 候选快照落盘文件名（FR-148，落 agent 数据目录，与配置快照平行）。 */
    private const val CANDIDATES_SNAPSHOT_FILE = "candidates-snapshot.json"

    /** 交付本地备份区目录名（FR-165，spec §4.7.1；落 agent 自身 dataFolder 之下）。 */
    private const val DELIVERY_BACKUPS_DIR = "delivery-backups"

    /** 交付下载临时目录名（FR-165，spec §4.5.3；落 agent 自身 dataFolder 之下，校验齐全才覆盖）。 */
    private const val DELIVERY_TMP_DIR = "delivery-tmp"
}
