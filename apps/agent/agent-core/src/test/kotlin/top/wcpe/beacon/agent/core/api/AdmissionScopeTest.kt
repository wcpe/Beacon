package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.api.AdmissionScope
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * 准入作用域取值不变量的单测（FR-244）。
 *
 * 锁四件事：
 * ① 判定方向：备选之间 OR、备选之内 AND、值逐字符精确相等（不做 trim / 大小写折叠）；
 * ② 「空」只有一种形态：恒真与 [AdmissionScope.isEmpty] 永远一致，不存在"非空却恒真"的中间态；
 * ③ 入口规约：含空备选的 OR、并列空作用域都退回 empty()（OR 之下"不限制"吞掉一切）；
 * ④ 值对象不可变、键非空白。
 */
class AdmissionScopeTest {
    @Test
    fun `备选之间是或——任一备选成立即准入`() {
        val scope = AdmissionScope.anyOf(listOf(mapOf("k" to "v"), mapOf("k2" to "v2")))

        assertTrue(scope.admits(mapOf("k" to "v")), "第一个备选成立即准入")
        assertTrue(scope.admits(mapOf("k2" to "v2")), "第二个备选成立即准入")
        assertTrue(scope.admits(mapOf("k" to "v", "k2" to "v2")), "两个都成立自然也准入")
        assertFalse(scope.admits(mapOf("k" to "other")), "两个备选都不成立才拒绝")
        assertFalse(scope.admits(emptyMap()), "没有任何声明则不满足非空备选")
    }

    @Test
    fun `备选之内是与——同一备选的所有键值都要命中`() {
        val scope = AdmissionScope.of(mapOf("k" to "v", "k2" to "v2"))

        assertEquals(1, scope.alternatives().size)
        assertTrue(scope.admits(mapOf("k" to "v", "k2" to "v2")), "全部命中才成立")
        assertFalse(scope.admits(mapOf("k" to "v")), "缺一个键不成立")
        assertFalse(scope.admits(mapOf("k" to "v", "k2" to "other")), "值不对不成立")
    }

    @Test
    fun `值按逐字符精确相等比较——大小写与空白都不折叠`() {
        val scope = AdmissionScope.of("k", "Lobby")

        assertTrue(scope.admits(mapOf("k" to "Lobby")))
        assertFalse(scope.admits(mapOf("k" to "lobby")), "大小写不折叠（归一化只在写入声明那一方做）")
        assertFalse(scope.admits(mapOf("k" to " Lobby")), "前后空白不 trim")
        assertFalse(scope.admits(mapOf("k" to "Lobby ")), "前后空白不 trim")
        assertFalse(scope.admits(mapOf("other" to "Lobby")), "键也要精确命中")
    }

    @Test
    fun `未声明的节点一律不满足非空备选`() {
        val scope = AdmissionScope.of("k", "v")

        assertFalse(scope.admits(null), "「读不到声明」不得读成「满足」，否则准入静默失效")
        assertFalse(scope.admits(emptyMap()), "声明为空同样不满足")
    }

    @Test
    fun `空作用域与空备选列表都恒真`() {
        val empty = AdmissionScope.empty()
        val emptyList = AdmissionScope.anyOf(emptyList<Map<String, String>>())

        assertTrue(empty.isEmpty)
        assertTrue(emptyList.isEmpty)
        assertEquals(empty, emptyList, "空的写法只有一种形态")
        listOf(null, emptyMap<String, String>(), mapOf("k" to "v")).forEach {
            assertTrue(empty.admits(it), "空作用域不排除任何候选")
            assertTrue(emptyList.admits(it), "空备选列表等价于空作用域")
        }
    }

    @Test
    fun `含恒真备选的或作用域退化为空——isEmpty 与判定结果一致`() {
        val constantTrue = AdmissionScope.anyOf(listOf(emptyMap<String, String>()))

        assertTrue(constantTrue.isEmpty, "「恒真 ∨ 任意」恒真：不得呈现「非空却恒真」的中间态")
        assertEquals(AdmissionScope.empty(), constantTrue, "退化后与 empty() 相等")
        assertEquals(AdmissionScope.empty().hashCode(), constantTrue.hashCode())
        assertTrue(constantTrue.admits(null), "不排除任何候选")
        assertTrue(constantTrue.alternatives().isEmpty(), "不得把空备选下发到协议里")
    }

    @Test
    fun `与恒真备选并列的具体备选同样被吞掉`() {
        val scope = AdmissionScope.anyOf(listOf(mapOf("k" to "v"), emptyMap<String, String>()))

        assertTrue(scope.isEmpty, "空备选在任何位置都让整个作用域失去约束力")
        assertEquals(AdmissionScope.empty(), scope)
        assertTrue(scope.admits(emptyMap()), "本该全放行")
    }

    @Test
    fun `或作用域并列任一空作用域即退化为空`() {
        val scope = AdmissionScope.anyOf(AdmissionScope.empty(), AdmissionScope.of("k", "v"))

        assertTrue(scope.isEmpty, "「不限制 ∨ 某个取值」就是「不限制」，方向不得反成静默收紧")
        assertEquals(AdmissionScope.empty(), scope)
        assertTrue(scope.admits(emptyMap()), "配置缺失就传 empty() 的写法必须保持全放行")
    }

    @Test
    fun `或作用域并列多个非空作用域仍是取并集`() {
        val scope =
            AdmissionScope.anyOf(
                AdmissionScope.of("k", "v"),
                AdmissionScope.of("k2", "v2"),
            )

        assertFalse(scope.isEmpty)
        assertEquals(2, scope.alternatives().size)
        assertTrue(scope.admits(mapOf("k" to "v")))
        assertTrue(scope.admits(mapOf("k2" to "v2")))
        assertFalse(scope.admits(mapOf("k" to "v2")))
    }

    @Test
    fun `作用域不可变——备选列表与内部 map 都不可改`() {
        val source = mutableMapOf("k" to "v")
        val scope = AdmissionScope.of(source)
        source["k"] = "changed"

        assertEquals(mapOf("k" to "v"), scope.alternatives().single(), "构造后外部改动不得影响作用域")
        assertFailsWith<UnsupportedOperationException> { scope.alternatives().single()["k"] = "x" }
        assertFailsWith<UnsupportedOperationException> {
            (scope.alternatives() as MutableList<Map<String, String>>).add(mapOf("k2" to "v2"))
        }
    }

    @Test
    fun `anyOf 列表入参的后续改动不影响作用域`() {
        val sourceAfter = mutableMapOf("k2" to "v2")
        val scope = AdmissionScope.anyOf(listOf(mapOf("k" to "v"), sourceAfter))
        sourceAfter["k2"] = "changed"

        assertEquals(2, scope.alternatives().size)
        assertTrue(scope.admits(mapOf("k2" to "v2")), "防御性拷贝：外部改动不得改判定")
    }

    @Test
    fun `键不能为空白——空白键在入口被拒`() {
        assertFailsWith<IllegalArgumentException> { AdmissionScope.of(" ", "v") }
        assertFailsWith<IllegalArgumentException> { AdmissionScope.of(mapOf("\t" to "v")) }
        assertFailsWith<IllegalArgumentException> {
            AdmissionScope.anyOf(listOf(mapOf("k" to "v"), mapOf("  " to "v2")))
        }
    }

    @Test
    fun `键为空白时即便同时存在恒真备选也照实拒绝`() {
        assertFailsWith<IllegalArgumentException> {
            AdmissionScope.anyOf(listOf(emptyMap<String, String>(), mapOf(" " to "v")))
        }
    }

    @Test
    fun `值可以为空串——空串是合法取值不等同于未声明`() {
        val scope = AdmissionScope.of("k", "")

        assertFalse(scope.isEmpty)
        assertTrue(scope.admits(mapOf("k" to "")))
        assertFalse(scope.admits(mapOf("k" to "v")))
        assertFalse(scope.admits(emptyMap()), "键缺失与键存在但值为空串是两回事")
    }
}
