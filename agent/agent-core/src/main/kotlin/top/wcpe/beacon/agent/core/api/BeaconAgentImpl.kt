package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.AgentIdentity as ApiIdentity
import top.wcpe.beacon.agent.api.BeaconAgent
import top.wcpe.beacon.agent.api.Discovery
import top.wcpe.beacon.agent.api.EffectiveConfig
import top.wcpe.beacon.agent.api.Messaging
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.lifecycle.AgentLifecycle
import top.wcpe.beacon.agent.core.messaging.MessagingHolder
import java.util.Optional

/**
 * BeaconAgent 门面的 core 实现，两个平台壳共用（避免重复）。
 *
 * identity 的 group/zone 取自实时 store（控制面解析的权威值），保证换区后读到新归属。
 */
class BeaconAgentImpl(
    private val identity: AgentIdentity,
    private val store: EffectiveConfigStore,
    private val lifecycle: AgentLifecycle,
    private val effectiveConfig: EffectiveConfig,
    private val discovery: Discovery,
    private val messagingHolder: MessagingHolder,
) : BeaconAgent {

    override fun identity(): ApiIdentity {
        // group/zone 以 store 当前值为准（注册/拉取后回填、换区后更新）。
        return ApiIdentity(
            identity.namespace,
            identity.serverId,
            identity.role,
            store.currentGroup(),
            store.currentZone(),
        )
    }

    override fun config(): EffectiveConfig = effectiveConfig

    override fun discovery(): Discovery = discovery

    // 始终返回当前生效门面（未启用时为 DisabledMessaging，isAvailable=false）。
    override fun messaging(): Messaging = messagingHolder.get()

    override fun connected(): Boolean = lifecycle.isConnected()

    override fun awaitRegistered(timeoutMillis: Long): Boolean = lifecycle.awaitFirstRegister(timeoutMillis)

    override fun effectiveMd5(): Optional<String> = Optional.ofNullable(store.currentMd5())
}
