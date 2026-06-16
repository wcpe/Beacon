package top.wcpe.beacon.agent.adapters

import top.wcpe.beacon.agent.core.transport.JsonCodec
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.doubleOrNull
import kotlinx.serialization.json.longOrNull

/**
 * 基于 kotlinx.serialization 的 JsonCodec 实现（ADR-0005 唯一碰具体库的类之一）。
 *
 * 用 JsonElement 在「泛型树（Map/List/基本类型/null）」与 json 文本间互转，
 * 使 core 无需引入 @Serializable 与 kotlinx 类型。
 */
class KotlinxJsonCodec : JsonCodec {

    private val json = Json {
        // 容忍服务端可能多出的字段；编码时省略 null 由我们手动控制（见 toElement）。
        ignoreUnknownKeys = true
        encodeDefaults = true
    }

    override fun encode(value: Any?): String {
        return json.encodeToString(JsonElement.serializer(), toElement(value))
    }

    override fun decode(json: String): Any? {
        val element = this.json.parseToJsonElement(json)
        return fromElement(element)
    }

    /** 泛型树 → JsonElement。 */
    private fun toElement(value: Any?): JsonElement {
        return when (value) {
            null -> JsonNull
            is JsonElement -> value
            is String -> JsonPrimitive(value)
            is Boolean -> JsonPrimitive(value)
            is Number -> JsonPrimitive(value)
            is Map<*, *> -> {
                val content = LinkedHashMap<String, JsonElement>()
                for ((k, v) in value) {
                    content[k.toString()] = toElement(v)
                }
                JsonObject(content)
            }

            is List<*> -> JsonArray(value.map { toElement(it) })
            else -> JsonPrimitive(value.toString())
        }
    }

    /** JsonElement → 泛型树（Map/List/基本类型/null）。 */
    private fun fromElement(element: JsonElement): Any? {
        return when (element) {
            is JsonNull -> null
            is JsonObject -> {
                val map = LinkedHashMap<String, Any?>()
                for ((k, v) in element) {
                    map[k] = fromElement(v)
                }
                map
            }

            is JsonArray -> element.map { fromElement(it) }
            is JsonPrimitive -> primitiveToValue(element)
        }
    }

    /** JsonPrimitive → String / Boolean / Long / Double。 */
    private fun primitiveToValue(p: JsonPrimitive): Any? {
        // 带引号即字符串。
        if (p.isString) return p.content
        // 布尔。
        p.booleanOrNull?.let { return it }
        // 整数优先（避免把 200 解析成 200.0）。
        p.longOrNull?.let { return it }
        // 浮点。
        p.doubleOrNull?.let { return it }
        // 兜底：原文。
        return p.content
    }
}
