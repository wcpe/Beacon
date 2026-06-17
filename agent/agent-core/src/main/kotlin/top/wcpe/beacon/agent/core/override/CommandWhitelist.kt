package top.wcpe.beacon.agent.core.override

/**
 * agent 本地命令白名单（ADR-0011 决策 3，反向制衡）。
 *
 * 白名单首 token 集合放 **agent 本地 config.yml**、**不由控制面下发**——控制面被攻破也无法越白名单提权。
 * **默认空白名单**（构造空集即一律拒绝命令派发）。
 *
 * 放行前置硬约束（防注入）：
 * - 单条命令：含 `; & | > < $` 反引号、换行 / 回车 / 制表符等任何控制字符一律拒绝；
 * - 首 token（按空白切分第一段）须命中白名单，**不区分大小写**；不做子串 / 整句匹配。
 *
 * 不可变值对象，无副作用。
 */
class CommandWhitelist(
    allowedFirstTokens: Set<String>,
) {

    // 归一化为小写，比对时不区分大小写。
    private val allowed: Set<String> = allowedFirstTokens.map { it.trim().lowercase() }.filter { it.isNotEmpty() }.toSet()

    /** 白名单是否为空（默认态；为空表示命令派发能力实质关闭）。 */
    fun isEmpty(): Boolean = allowed.isEmpty()

    /**
     * 判断一条命令是否允许派发：先过注入字符闸，再按首 token 命中白名单（不区分大小写）。
     */
    fun isAllowed(command: String): Boolean {
        val trimmed = command.trim()
        if (trimmed.isEmpty()) return false
        if (containsForbiddenChar(trimmed)) return false
        val firstToken = trimmed.split(WHITESPACE).firstOrNull()?.lowercase() ?: return false
        if (firstToken.isEmpty()) return false
        return allowed.contains(firstToken)
    }

    /** 是否含禁止字符：元字符或任何控制字符（ASCII < 0x20 或 0x7F）。 */
    private fun containsForbiddenChar(s: String): Boolean {
        for (c in s) {
            if (c in META_CHARS) return true
            if (c.code < 0x20 || c.code == 0x7F) return true
        }
        return false
    }

    companion object {
        /** 注入元字符集合（管道 / 重定向 / 变量 / 反引号 / 与或 / 后台）。 */
        private val META_CHARS: Set<Char> = setOf(';', '&', '|', '>', '<', '$', '`')

        /** 空白切分（含空格 / 制表，但制表已在禁止字符里拦掉）。 */
        private val WHITESPACE = Regex("\\s+")
    }
}
