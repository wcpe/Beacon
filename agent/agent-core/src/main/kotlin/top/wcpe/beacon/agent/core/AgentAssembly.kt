package top.wcpe.beacon.agent.core

import top.wcpe.beacon.agent.api.BeaconAgent
import top.wcpe.beacon.agent.core.api.BeaconAgentImpl
import top.wcpe.beacon.agent.core.api.DiscoveryView
import top.wcpe.beacon.agent.core.api.EffectiveConfigView
import top.wcpe.beacon.agent.core.api.TopologyWatchHub
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.config.ConfigApplier
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.filetree.AppliedFileManifestStore
import top.wcpe.beacon.agent.core.filetree.FileMirrorWriter
import top.wcpe.beacon.agent.core.filetree.FileTreeApplier
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.lifecycle.AgentLifecycle
import top.wcpe.beacon.agent.core.override.CommandWhitelist
import top.wcpe.beacon.agent.core.override.OverrideSyncApplier
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.snapshot.SnapshotStore
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import top.wcpe.beacon.agent.core.transport.StreamTransport
import java.io.File

/**
 * 装配产物：把 lifecycle 与对外门面交回壳层。
 */
class AssembledAgent(
    val lifecycle: AgentLifecycle,
    val beaconAgent: BeaconAgent,
    val apiClient: BeaconApiClient,
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
        adapter: PlatformAdapter,
        transport: HttpTransport,
        codec: JsonCodec,
        store: EffectiveConfigStore,
        effectiveConfigView: EffectiveConfigView,
        // 流式传输（SSE 推送，FR-24）：壳层注入 OkHttpStreamTransport 即启用单条流取代三条长轮询；
        // 为 null 时退回长轮询（迁移期兼容，见 ADR-0015 决策 8）。
        streamTransport: StreamTransport? = null,
    ): AssembledAgent {
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
        )

        val discoveryView = DiscoveryView(apiClient, topologyWatchHub)
        val beaconAgent = BeaconAgentImpl(identity, store, lifecycle, effectiveConfigView, discoveryView)

        return AssembledAgent(lifecycle, beaconAgent, apiClient)
    }
}
