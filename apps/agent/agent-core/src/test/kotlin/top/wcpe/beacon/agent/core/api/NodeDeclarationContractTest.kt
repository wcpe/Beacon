package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.DeclarationApplied
import top.wcpe.beacon.agent.api.DeclarationOutcome
import top.wcpe.beacon.agent.api.NodeDeclaration
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * agent-api 声明契约的取值不变量单测（FR-243）：部分刷新语义（缺省 ≠ 刷成空）与不可变性；
 * 三类结论取值互斥（同一个返回值绝不承载两类失败）。
 */
class NodeDeclarationContractTest {
    @Test
    fun `capacity 的缺省与 0 可区分`() {
        assertTrue(NodeDeclaration.ofLabels(mapOf("k" to "v")).capacity().isEmpty, "未给容量应表达「不刷新」")
        assertEquals(0, NodeDeclaration.ofCapacity(0).capacity().get(), "容量 0 是合法值，不等于「不刷新」")
    }

    @Test
    fun `labels 的缺省与空集合可区分`() {
        val capacityOnly = NodeDeclaration.ofCapacity(1)
        assertFalse(capacityOnly.hasLabels(), "未给标签应表达「不刷新」")
        assertTrue(capacityOnly.labels().isEmpty())

        val cleared = NodeDeclaration.ofLabels(emptyMap())
        assertTrue(cleared.hasLabels(), "空集合是「提供」，语义为清空全部标签")
        assertTrue(cleared.labels().isEmpty())
    }

    @Test
    fun `标签为不可修改的防御性拷贝`() {
        val source = mutableMapOf("mode" to "beta")
        val declaration = NodeDeclaration.ofLabels(source)
        source["mode"] = "stable"

        assertEquals(mapOf("mode" to "beta"), declaration.labels(), "构造后外部改动不得影响声明")
        assertFailsWith<UnsupportedOperationException> { declaration.labels()["x"] = "y" }
    }

    @Test
    fun `生效标签同样不可变`() {
        val source = mutableMapOf("mode" to "beta")
        val applied = DeclarationApplied(64, source)
        source["mode"] = "stable"

        assertEquals(mapOf("mode" to "beta"), applied.labels())
        assertFailsWith<UnsupportedOperationException> { applied.labels()["x"] = "y" }
    }

    @Test
    fun `三类结论取值互斥`() {
        val applied = DeclarationOutcome.applied(DeclarationApplied(1, emptyMap()))
        assertEquals(DeclarationOutcome.Status.APPLIED, applied.status())
        assertTrue(applied.applied().isPresent)
        assertFalse(applied.rejectReason().isPresent, "APPLIED 不得携带拒绝原因")

        val rejected = DeclarationOutcome.rejected("INVALID_PARAM")
        assertEquals(DeclarationOutcome.Status.REJECTED, rejected.status())
        assertEquals("INVALID_PARAM", rejected.rejectReason().get())
        assertFalse(rejected.applied().isPresent, "REJECTED 不得携带生效值")

        val unavailable = DeclarationOutcome.unavailable()
        assertEquals(DeclarationOutcome.Status.UNAVAILABLE, unavailable.status())
        assertFalse(unavailable.applied().isPresent, "UNAVAILABLE 与 REJECTED 不得共用一个取值")
        assertFalse(unavailable.rejectReason().isPresent, "UNAVAILABLE 不得复用拒绝原因的取值")
    }
}
