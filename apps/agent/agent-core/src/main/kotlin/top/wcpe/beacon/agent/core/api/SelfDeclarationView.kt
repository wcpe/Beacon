package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.DeclarationOutcome
import top.wcpe.beacon.agent.api.NodeDeclaration
import top.wcpe.beacon.agent.api.SelfDeclaration
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.DeclarationResult
import top.wcpe.beacon.agent.core.client.declare
import top.wcpe.beacon.agent.core.identity.AgentIdentity

/**
 * 节点自声明门面的控制面实现（FR-243，见 ADR-0086）：把声明经既有 HTTP 通道
 * （POST /beacon/v1/agent/declaration）发往控制面，并把 core 结果映射为门面三档取值。
 *
 * 只做映射与告警，不含重试与状态：**重试语义由调用方按 [DeclarationOutcome.Status] 决定**；
 * 「未注册成功」等时机问题由 [NodeDeclarationHolder] 负责（降级 + 注册后补报）。
 *
 * 同步 HTTP，仅在异步线程调用（绝不在 MC 主线程使用）。
 *
 * @param warn WARN 日志回调（拒绝 / 通道不可用时各记一行脱敏原因，便于运维看清「为什么没成功」）
 */
internal class SelfDeclarationView(
    private val apiClient: BeaconApiClient,
    private val identity: AgentIdentity,
    private val warn: (String) -> Unit = {},
) : SelfDeclaration {
    override fun declare(declaration: NodeDeclaration?): DeclarationOutcome {
        // 门面契约「绝不外抛」：空声明按「无有效字段」拒绝——这是稳定事实（改正后重报），非通道故障。
        if (declaration == null) return DeclarationOutcome.rejected(NULL_DECLARATION)

        return when (val result = apiClient.declare(identity, declaration)) {
            is DeclarationResult.Applied -> DeclarationOutcome.applied(result.applied)
            is DeclarationResult.Rejected -> {
                warn("节点声明被拒（serverId=${identity.serverId}）：${result.reason}")
                DeclarationOutcome.rejected(result.reason)
            }

            is DeclarationResult.Unavailable -> {
                warn("节点声明通道不可用（serverId=${identity.serverId}）：${result.reason}")
                DeclarationOutcome.unavailable()
            }
        }
    }

    private companion object {
        /** 调用方未提供任何声明字段（两类字段全缺）时的拒绝原因。 */
        const val NULL_DECLARATION = "未提供任何声明字段（capacity 与 labels 至少给一项）"
    }
}
