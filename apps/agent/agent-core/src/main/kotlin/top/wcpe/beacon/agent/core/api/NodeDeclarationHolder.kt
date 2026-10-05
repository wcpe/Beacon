package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.DeclarationOutcome
import top.wcpe.beacon.agent.api.NodeDeclaration
import top.wcpe.beacon.agent.api.SelfDeclaration

/**
 * 可选自声明实现的可变持有者（FR-243），本身即一个降级版 [SelfDeclaration]。
 *
 * [BeaconAgentImpl] 在装配期即持有本对象，而真实的控制面实现要等注册成功才可用（声明端点按在册身份判定，
 * 未注册时必被 401 / 404 拒绝），故装配期是降级态、注册成功后由生命周期钩子 [set] 实际实现、停机时 [reset] 复位。
 *
 * **启动竞态**：降级态下调用 [declare] 一律如实回 [DeclarationOutcome.Status.UNAVAILABLE]（可重试、绝不外抛），
 * 并记住**最近一次**声明；注册成功后 [set] 自动补报这一次（幂等刷新，重复补报无副作用），
 * 避免业务插件在插件启动早期发起的声明静默丢失。
 *
 * @param warn WARN 日志回调（补报失败 / 实现异常时各记一行，默认无操作）
 */
class NodeDeclarationHolder(
    private val warn: (String) -> Unit = {},
) : SelfDeclaration {
    /** 当前真实实现；null 表示未就绪（装配期 / 尚未注册成功 / 已停机）的降级态。 */
    @Volatile
    private var current: SelfDeclaration? = null

    /**
     * 缓冲与交接的互斥锁：只覆盖「读 [current] / 记 / 取 [pending]」这一小段，
     * **绝不覆盖声明调用本身**（网络 IO 不得持锁，否则会拖住注册成功钩子与停机）。
     */
    private val gate = Any()

    /** 未就绪期记住的最近一次声明（注册成功后自动补报一次）。仅在 [gate] 内读写。 */
    private var pending: NodeDeclaration? = null

    /**
     * 切换为真实实现（注册成功后由生命周期钩子注入），并自动补报未就绪期记住的最近一次声明。
     */
    fun set(declaration: SelfDeclaration) {
        val buffered =
            synchronized(gate) {
                current = declaration
                // 取走待补报项：与 declare 的交接在同一把锁内完成，保证「记录 → 补报」之间不丢一次声明。
                pending.also { pending = null }
            }
        if (buffered != null) replay(declaration, buffered)
    }

    /** 复位为降级态（停机时调用）；随停机丢弃待补报项（下次启动的注册成功不该补报上一次运行的声明）。 */
    fun reset() {
        synchronized(gate) {
            current = null
            pending = null
        }
    }

    /**
     * 声明（刷新）本节点容量与 / 或标签；未就绪时回 [DeclarationOutcome.Status.UNAVAILABLE] 并记住本次声明。
     */
    override fun declare(declaration: NodeDeclaration?): DeclarationOutcome {
        val delegate =
            synchronized(gate) {
                val active = current
                // 未就绪：记住最近一次声明备补报；null 不记，避免无效调用抹掉有效声明。
                if (active == null && declaration != null) pending = declaration
                active
            }
        return delegate?.let { forward(it, declaration) } ?: DeclarationOutcome.unavailable()
    }

    /** 透传给真实实现；实现异常一律降级为 UNAVAILABLE（门面契约「绝不外抛」）。 */
    private fun forward(
        delegate: SelfDeclaration,
        declaration: NodeDeclaration?,
    ): DeclarationOutcome =
        try {
            delegate.declare(declaration)
        } catch (t: Throwable) {
            warn("节点声明调用异常，降级为 UNAVAILABLE：${t.message}")
            DeclarationOutcome.unavailable()
        }

    /** 注册成功后补报一次；失败不改注册结果（僵尸声明由接入方下次重报覆盖）。 */
    private fun replay(
        delegate: SelfDeclaration,
        buffered: NodeDeclaration,
    ) {
        try {
            delegate.declare(buffered)
        } catch (t: Throwable) {
            warn("注册成功后补报节点声明异常（已忽略）：${t.message}")
        }
    }
}
