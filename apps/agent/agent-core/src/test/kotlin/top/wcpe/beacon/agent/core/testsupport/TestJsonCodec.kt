package top.wcpe.beacon.agent.core.testsupport

import top.wcpe.beacon.agent.core.transport.JsonCodec

/**
 * 测试用的最小 JSON 编解码器。
 *
 * ## 为什么测试里要自带一个
 *
 * 真实实现（`KotlinxJsonCodec`）在 `agent-adapters` 模块，`agent-core` 不能在测试里依赖它
 * （依赖方向是 adapters → core，反向会成环）。而 [JsonCodec] 是个只有两个方法的窄接口，
 * 为测试实现一份成本极低，还能顺带证明「这个抽象确实可以脱离 kotlinx 使用」。
 *
 * ## 支持范围
 *
 * 只覆盖 `Map<String,Any?>` / `List<Any?>` / String / Number / Boolean / null 的双向转换
 * ——这正是 [JsonCodec] 文档声明的契约范围。刻意保持最小，不在测试里堆一个完整 JSON 解析器。
 */
object TestJsonCodec : JsonCodec {
    override fun encode(value: Any?): String {
        val sb = StringBuilder()
        write(value, sb)
        return sb.toString()
    }

    override fun decode(json: String): Any? {
        val parser = Parser(json)
        val value = parser.readValue()
        parser.skipWhitespace()
        return value
    }

    private fun write(
        value: Any?,
        sb: StringBuilder,
    ) {
        when (value) {
            null -> sb.append("null")
            is String -> writeString(value, sb)
            is Boolean -> sb.append(if (value) "true" else "false")
            is Number -> sb.append(value.toString())
            is Map<*, *> -> {
                sb.append('{')
                var first = true
                for ((k, v) in value) {
                    if (!first) sb.append(',')
                    first = false
                    writeString(k.toString(), sb)
                    sb.append(':')
                    write(v, sb)
                }
                sb.append('}')
            }
            is Iterable<*> -> {
                sb.append('[')
                var first = true
                for (item in value) {
                    if (!first) sb.append(',')
                    first = false
                    write(item, sb)
                }
                sb.append(']')
            }
            else -> writeString(value.toString(), sb)
        }
    }

    private fun writeString(
        raw: String,
        sb: StringBuilder,
    ) {
        sb.append('"')
        for (ch in raw) {
            when (ch) {
                '"' -> sb.append("\\\"")
                '\\' -> sb.append("\\\\")
                '\n' -> sb.append("\\n")
                '\r' -> sb.append("\\r")
                '\t' -> sb.append("\\t")
                else -> sb.append(ch)
            }
        }
        sb.append('"')
    }

    /** 极简递归下降解析器（只为测试服务，不追求错误信息友好）。 */
    private class Parser(
        private val text: String,
    ) {
        private var pos = 0

        fun skipWhitespace() {
            while (pos < text.length && text[pos].isWhitespace()) pos++
        }

        fun readValue(): Any? {
            skipWhitespace()
            if (pos >= text.length) error("意外的输入结束")
            return when (text[pos]) {
                '{' -> readObject()
                '[' -> readArray()
                '"' -> readString()
                't' -> readLiteral("true", true)
                'f' -> readLiteral("false", false)
                'n' -> readLiteral("null", null)
                else -> readNumber()
            }
        }

        private fun readObject(): Map<String, Any?> {
            expect('{')
            val out = LinkedHashMap<String, Any?>()
            skipWhitespace()
            if (peek() == '}') {
                pos++
                return out
            }
            while (true) {
                skipWhitespace()
                val key = readString()
                expect(':')
                out[key] = readValue()
                skipWhitespace()
                when (peek()) {
                    ',' -> pos++
                    '}' -> {
                        pos++
                        return out
                    }
                    else -> error("对象结构非法：位置 $pos")
                }
            }
        }

        private fun readArray(): List<Any?> {
            expect('[')
            val out = mutableListOf<Any?>()
            skipWhitespace()
            if (peek() == ']') {
                pos++
                return out
            }
            while (true) {
                out += readValue()
                skipWhitespace()
                when (peek()) {
                    ',' -> pos++
                    ']' -> {
                        pos++
                        return out
                    }
                    else -> error("数组结构非法：位置 $pos")
                }
            }
        }

        private fun readString(): String {
            expect('"')
            val sb = StringBuilder()
            while (pos < text.length) {
                when (val ch = text[pos]) {
                    '"' -> {
                        pos++
                        return sb.toString()
                    }
                    '\\' -> {
                        pos++
                        if (pos >= text.length) error("转义不完整")
                        when (val esc = text[pos]) {
                            '"' -> sb.append('"')
                            '\\' -> sb.append('\\')
                            'n' -> sb.append('\n')
                            'r' -> sb.append('\r')
                            't' -> sb.append('\t')
                            else -> sb.append(esc)
                        }
                        pos++
                    }
                    else -> {
                        sb.append(ch)
                        pos++
                    }
                }
            }
            error("字符串未闭合")
        }

        private fun readNumber(): Number {
            val start = pos
            while (pos < text.length && isNumberChar(text[pos])) pos++
            val raw = text.substring(start, pos)
            return raw.toLongOrNull() ?: raw.toDoubleOrNull() ?: error("不是数字：$raw")
        }

        private fun readLiteral(
            literal: String,
            value: Any?,
        ): Any? {
            require(text.startsWith(literal, pos)) { "期望 $literal" }
            pos += literal.length
            return value
        }

        /** 数字可接受的字符：数字、负号、小数点（够用即可，不追求完整 JSON 数字语法）。 */
        private fun isNumberChar(ch: Char): Boolean = ch.isDigit() || ch == '-' || ch == '.'

        private fun peek(): Char = if (pos < text.length) text[pos] else error("意外的输入结束")

        private fun expect(ch: Char) {
            skipWhitespace()
            require(pos < text.length && text[pos] == ch) { "期望 '$ch'，位置 $pos" }
            pos++
        }
    }
}
