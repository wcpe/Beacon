package com.beacon.agent.core

/**
 * agent-core 占位入口。
 *
 * 纯 Kotlin，不依赖任何 Minecraft 平台与 TabooLib。
 * M0 仅留版本常量占位，传输/编解码抽象（HttpTransport / JsonCodec，见 ADR-0005）在 M5 实现。
 */
object AgentCore {

    /** agent 版本号，与主项目保持一致。 */
    const val VERSION: String = "0.1.0"
}
