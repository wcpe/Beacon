package com.beacon.agent.core.transport

/**
 * JSON 编解码抽象（ADR-0005，落实解耦意图）。
 *
 * 为让 core 不碰 kotlinx 的 @Serializable / 强类型，采用泛型树而非强类型：
 * - encode：Map<String,Any?> / List<Any?> / String / Number / Boolean / null → json 文本
 * - decode：json 文本 → Map<String,Any?> / List<Any?> / 基本类型 / null
 *
 * 具体实现（KotlinxJsonCodec）在 agent-adapters，core 内不得出现 @Serializable 类型。
 */
interface JsonCodec {

    /** 将泛型树编码为 json 文本。 */
    fun encode(value: Any?): String

    /** 将 json 文本解码为泛型树。 */
    fun decode(json: String): Any?
}
