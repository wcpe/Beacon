package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.DeclarationOutcome
import top.wcpe.beacon.agent.api.NodeDeclaration
import top.wcpe.beacon.agent.api.SelfDeclaration

/**
 * 自声明门面的未装配占位实现（FR-243）：装配未注入真实实现时的兜底默认。
 *
 * 语义即极端 fail-static：一律回 [DeclarationOutcome.Status.UNAVAILABLE]（可重试），不抛、不阻塞。
 * 生产装配注入的是 [NodeDeclarationHolder]（未注册成功前同样是本语义，且会记住最近一次声明、
 * 注册成功后自动补报一次）。
 */
object UnavailableSelfDeclaration : SelfDeclaration {
    override fun declare(declaration: NodeDeclaration?): DeclarationOutcome = DeclarationOutcome.unavailable()
}
