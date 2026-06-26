package top.wcpe.beacon.agent.core.command

import kotlin.test.Test
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * 文本/二进制按名启发判定纯函数 [TextFileHeuristic] 单测（FR-109）：
 * 反向抓取 scan 与只读浏览共用同一口径。
 */
class TextFileHeuristicTest {

    @Test
    fun `常见文本扩展名判文本`() {
        assertTrue(TextFileHeuristic.looksTextByName("config.yml"))
        assertTrue(TextFileHeuristic.looksTextByName("lang/zh_CN.yml"))
        assertTrue(TextFileHeuristic.looksTextByName("data.json"))
        assertTrue(TextFileHeuristic.looksTextByName("notes.txt"))
        // 无扩展名保守按文本（如 README）。
        assertTrue(TextFileHeuristic.looksTextByName("README"))
    }

    @Test
    fun `常见二进制扩展名判非文本`() {
        assertFalse(TextFileHeuristic.looksTextByName("plugin.jar"))
        assertFalse(TextFileHeuristic.looksTextByName("icon.png"))
        assertFalse(TextFileHeuristic.looksTextByName("data.DB"))
        assertFalse(TextFileHeuristic.looksTextByName("region.MCA"))
        assertFalse(TextFileHeuristic.looksTextByName("lib.so"))
    }
}
