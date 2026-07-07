package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.RegisterOutcome
import top.wcpe.beacon.agent.core.client.RegistrationPollResult
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.settings.AgentSettings
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicReference

internal data class PendingRegistrationRuntime(
    val state: AtomicReference<AgentState>,
    val running: AtomicBoolean,
    val registering: AtomicBoolean,
)

internal data class PendingRegistrationActions(
    val registerNow: () -> Unit,
)

internal class PendingRegistrationController(
    private val identity: AgentIdentity,
    private val settings: AgentSettings,
    private val adapter: PlatformAdapter,
    private val apiClient: BeaconApiClient,
    private val runtime: PendingRegistrationRuntime,
    private val actions: PendingRegistrationActions,
) {
    fun waitForApproval(outcome: RegisterOutcome.PendingApproval) {
        runtime.state.set(AgentState.PENDING_APPROVAL)
        adapter.info("身份已提交待确认：namespace=${outcome.namespace}，serverId=${outcome.serverId}")
        when (apiClient.pollRegistration(identity, waitSeconds = 55)) {
            RegistrationPollResult.Active -> actions.registerNow()
            RegistrationPollResult.Pending,
            RegistrationPollResult.NotModified,
            -> retryPendingApproval()

            RegistrationPollResult.Disabled -> stopWithDegraded("身份确认后被禁用：${identity.serverId}，保持本地快照")
            RegistrationPollResult.Rejected -> stopWithDegraded("身份申请被拒绝：${identity.serverId}，停止自动重试")
            RegistrationPollResult.Conflict -> stopWithDegraded("身份确认等待期间进入冲突态：${identity.serverId}，等待后台处置")
            is RegistrationPollResult.Failed -> retryPendingApproval()
        }
    }

    private fun retryPendingApproval() {
        if (!runtime.running.get()) return
        adapter.runAsyncDelayed(settings.requestTimeoutMs) {
            if (runtime.running.get()) {
                waitForApproval(RegisterOutcome.PendingApproval(identity.serverId, identity.namespace))
            }
        }
    }

    private fun stopWithDegraded(message: String) {
        adapter.warn(message)
        runtime.state.set(AgentState.DEGRADED)
        runtime.registering.set(false)
    }
}
