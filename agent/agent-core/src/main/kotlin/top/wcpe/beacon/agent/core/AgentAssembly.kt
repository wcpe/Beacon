package top.wcpe.beacon.agent.core

import top.wcpe.beacon.agent.api.BeaconAgent
import top.wcpe.beacon.agent.core.api.BeaconAgentImpl
import top.wcpe.beacon.agent.core.api.DiscoveryView
import top.wcpe.beacon.agent.core.api.EffectiveConfigView
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.config.ConfigApplier
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.lifecycle.AgentLifecycle
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.snapshot.SnapshotStore
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.io.File

/**
 * 装配产物：把 lifecycle 与对外门面交回壳层。
 */
class AssembledAgent(
    val lifecycle: AgentLifecycle,
    val beaconAgent: BeaconAgent,
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
    ): AssembledAgent {
        val apiClient = BeaconApiClient(transport, codec, settings)

        val snapshotStore: SnapshotStore? = if (settings.snapshotEnabled) {
            SnapshotStore(File(adapter.dataFolder(), settings.snapshotFileName), codec)
        } else {
            null
        }

        val applier = ConfigApplier(store, snapshotStore, adapter)

        val lifecycle = AgentLifecycle(
            identity = identity,
            settings = settings,
            adapter = adapter,
            apiClient = apiClient,
            store = store,
            applier = applier,
            snapshotStore = snapshotStore,
        )

        val discoveryView = DiscoveryView(apiClient)
        val beaconAgent = BeaconAgentImpl(identity, store, lifecycle, effectiveConfigView, discoveryView)

        return AssembledAgent(lifecycle, beaconAgent)
    }
}
