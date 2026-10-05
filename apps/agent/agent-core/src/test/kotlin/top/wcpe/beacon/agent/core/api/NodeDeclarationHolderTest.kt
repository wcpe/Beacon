package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.DeclarationApplied
import top.wcpe.beacon.agent.api.DeclarationOutcome
import top.wcpe.beacon.agent.api.NodeDeclaration
import top.wcpe.beacon.agent.api.SelfDeclaration
import java.util.concurrent.CopyOnWriteArrayList
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * NodeDeclarationHolder 单测（FR-243，见规格 §3.3「启动竞态」）：
 * 装配期降级为 UNAVAILABLE 且不抛 → 注册成功后换真实实现 → 停机 reset 复位；
 * 未就绪期发起的声明记住最近一次、注册成功后自动补报**恰好一次**（幂等）。
 */
class NodeDeclarationHolderTest {
    /** 记录收到的声明并回 APPLIED 的替身；[fail] 用于模拟真实实现写入异常。 */
    private class RecordingDeclaration : SelfDeclaration {
        val declarations = CopyOnWriteArrayList<NodeDeclaration>()

        var fail: Boolean = false

        override fun declare(declaration: NodeDeclaration?): DeclarationOutcome {
            declarations.add(declaration!!)
            if (fail) error("模拟声明写入异常")
            return DeclarationOutcome.applied(
                DeclarationApplied(declaration.capacity().orElse(null), declaration.labels()),
            )
        }
    }

    // ---- 装配期降级 ----

    @Test
    fun `装配期声明回 UNAVAILABLE 且不外抛`() {
        val holder = NodeDeclarationHolder()

        val outcome = holder.declare(NodeDeclaration.ofCapacity(100))

        assertEquals(DeclarationOutcome.Status.UNAVAILABLE, outcome.status(), "未注册成功前应如实回通道不可用")
    }

    @Test
    fun `装配期重复声明不抛且都回 UNAVAILABLE`() {
        val holder = NodeDeclarationHolder()

        repeat(3) {
            assertEquals(
                DeclarationOutcome.Status.UNAVAILABLE,
                holder.declare(NodeDeclaration.ofLabels(mapOf("k$it" to "v"))).status(),
            )
        }
    }

    // ---- 注册成功后补报（启动竞态） ----

    @Test
    fun `注册成功后自动补报未就绪期最近一次声明恰好一次`() {
        val holder = NodeDeclarationHolder()
        val delegate = RecordingDeclaration()

        // 未就绪期连发三次：只记住最近一次。
        holder.declare(NodeDeclaration.ofCapacity(10))
        holder.declare(NodeDeclaration.ofCapacity(20))
        holder.declare(NodeDeclaration.ofCapacity(30))
        assertTrue(delegate.declarations.isEmpty(), "未就绪期不应外发声明")

        holder.set(delegate)

        assertEquals(1, delegate.declarations.size, "注册成功后应补报恰好一次")
        assertEquals(30, delegate.declarations[0].capacity().orElse(null), "补报的应是最近一次声明")
    }

    @Test
    fun `未就绪期没有声明时注册成功不补报`() {
        val holder = NodeDeclarationHolder()
        val delegate = RecordingDeclaration()

        holder.set(delegate)

        assertTrue(delegate.declarations.isEmpty(), "未就绪期无声明时不得凭空补报")
    }

    @Test
    fun `补报只发生一次重复 set 不重复补报`() {
        val holder = NodeDeclarationHolder()
        val delegate = RecordingDeclaration()
        holder.declare(NodeDeclaration.ofCapacity(10))

        holder.set(delegate)
        holder.set(RecordingDeclaration())

        assertEquals(1, delegate.declarations.size, "待补报项取走后不得重复补报")
    }

    @Test
    fun `补报失败不外抛且不影响注册流程`() {
        val warns = mutableListOf<String>()
        val holder = NodeDeclarationHolder(warn = warns::add)
        val delegate = RecordingDeclaration().apply { fail = true }
        holder.declare(NodeDeclaration.ofCapacity(10))

        holder.set(delegate)

        assertEquals(1, delegate.declarations.size, "补报已发起")
        assertTrue(warns.any { it.contains("补报") }, "补报失败须留痕：$warns")
    }

    // ---- 就绪后透传 ----

    @Test
    fun `set 之后声明透传到真实实现`() {
        val holder = NodeDeclarationHolder()
        val delegate = RecordingDeclaration()
        holder.set(delegate)

        val outcome = holder.declare(NodeDeclaration.of(64, mapOf("mode" to "beta")))

        assertEquals(DeclarationOutcome.Status.APPLIED, outcome.status())
        assertEquals(1, delegate.declarations.size)
        assertEquals(64, delegate.declarations[0].capacity().orElse(null))
        assertEquals(mapOf("mode" to "beta"), delegate.declarations[0].labels())
    }

    @Test
    fun `真实实现抛异常时降级为 UNAVAILABLE 而不外抛`() {
        val holder = NodeDeclarationHolder()
        val delegate = RecordingDeclaration().apply { fail = true }
        holder.set(delegate)

        val outcome = holder.declare(NodeDeclaration.ofCapacity(1))

        assertEquals(DeclarationOutcome.Status.UNAVAILABLE, outcome.status(), "实现异常应降级、绝不外抛")
    }

    // ---- 停机复位 ----

    @Test
    fun `reset 后回到降级态且不再触达真实实现`() {
        val holder = NodeDeclarationHolder()
        val delegate = RecordingDeclaration()
        holder.set(delegate)
        holder.declare(NodeDeclaration.ofCapacity(1))
        assertEquals(1, delegate.declarations.size)

        holder.reset()

        val outcome = holder.declare(NodeDeclaration.ofCapacity(2))
        assertEquals(DeclarationOutcome.Status.UNAVAILABLE, outcome.status(), "停机后应回到降级态")
        assertEquals(1, delegate.declarations.size, "停机后不得再触达真实实现")
    }
}
