package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.ActiveBinding
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.RegisterOutcome
import top.wcpe.beacon.agent.core.client.RegistrationPollResult
import top.wcpe.beacon.agent.core.client.bootstrapRegister
import top.wcpe.beacon.agent.core.client.pollRegistration
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.identity.IdentityBindingSnapshotStore
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.settings.AgentSettings
import java.util.concurrent.atomic.AtomicBoolean

/**
 * FR-203 引导期运行时：只做身份注册/轮询和快照校验，绝不装配数据面组件。
 */
class BootstrapRuntime(
    private val identity: AgentIdentity,
    private val settings: AgentSettings,
    private val adapter: PlatformAdapter,
    private val apiClient: BeaconApiClient,
    private val snapshots: IdentityBindingSnapshotStore,
    private val onActive: (AgentIdentity, ActiveBinding) -> Unit,
    private val onTerminal: () -> Unit = {},
) {
    private val running = AtomicBoolean(false)
    private val activeStarted = AtomicBoolean(false)
    private val terminated = AtomicBoolean(false)
    private val snapshotCandidate = snapshots.load(identity)

    fun start() {
        if (!running.compareAndSet(false, true)) return
        adapter.runAsync(::register)
    }

    fun shutdown() {
        running.set(false)
    }

    private fun register() {
        if (!running.get()) return
        when (val outcome = apiClient.bootstrapRegister(identity)) {
            is RegisterOutcome.ActiveBindingConfirmed -> confirmBinding(outcome.binding)
            is RegisterOutcome.PendingApproval -> waitForApproval()
            RegisterOutcome.Disabled,
            RegisterOutcome.Rejected,
            RegisterOutcome.IdentityConflict,
            RegisterOutcome.Unbound,
            -> terminal("控制面未授予 active 身份")

            RegisterOutcome.Unauthorized,
            RegisterOutcome.IdentityRequired,
            RegisterOutcome.DuplicateServerId,
            RegisterOutcome.OfflineRejected,
            is RegisterOutcome.Success,
            -> retryDelayed(::register)

            is RegisterOutcome.Failed -> {
                activateSnapshotIfAvailable()
                retryDelayed(::register)
            }
        }
    }

    private fun waitForApproval() {
        if (!running.get()) return
        when (val result = apiClient.pollRegistration(identity, WAIT_SECONDS)) {
            is RegistrationPollResult.Active -> confirmBinding(result.binding)
            RegistrationPollResult.Pending,
            RegistrationPollResult.NotModified,
            -> retryDelayed(::waitForApproval)

            RegistrationPollResult.Disabled,
            RegistrationPollResult.Rejected,
            RegistrationPollResult.Conflict,
            RegistrationPollResult.Unbound,
            -> terminal("控制面否决或解除身份绑定")

            is RegistrationPollResult.Failed -> {
                activateSnapshotIfAvailable()
                retryDelayed(::waitForApproval)
            }
        }
    }

    private fun activateSnapshotIfAvailable() {
        val snapshot = snapshotCandidate ?: return
        startActive(snapshot)
    }

    private fun confirmBinding(binding: ActiveBinding) {
        if (!running.get()) return
        if (snapshotCandidate != null && !sameAuthorityBinding(snapshotCandidate, binding)) {
            snapshots.invalidate()
            terminal("绑定快照与控制面权威绑定不一致")
            return
        }
        snapshots.write(identity, binding)
        startActive(binding)
    }

    private fun startActive(binding: ActiveBinding) {
        if (!running.get() || !activeStarted.compareAndSet(false, true)) return
        val activeIdentity = identity.copy(namespace = binding.namespace, serverId = binding.serverId, address = binding.compatAddress)
        onActive(activeIdentity, binding)
    }

    private fun sameAuthorityBinding(
        left: ActiveBinding,
        right: ActiveBinding,
    ): Boolean =
        left.namespace == right.namespace &&
            left.serverId == right.serverId &&
            left.boundAt == right.boundAt &&
            left.bindingFingerprint == right.bindingFingerprint

    private fun terminal(reason: String) {
        if (!terminated.compareAndSet(false, true)) return
        snapshots.invalidate()
        activeStarted.set(false)
        running.set(false)
        adapter.warn("$reason，停止 active runtime")
        onTerminal()
    }

    private fun retryDelayed(action: () -> Unit) {
        if (running.get()) adapter.runAsyncDelayed(settings.requestTimeoutMs, action)
    }

    private companion object {
        const val WAIT_SECONDS = 55
    }
}
