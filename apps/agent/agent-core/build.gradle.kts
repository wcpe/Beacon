// agent-core：平台无关核心。零具体库依赖（ADR-0005 硬边界）——
// 禁 okhttp、禁 kotlinx-serialization、禁 TabooLib、禁 Minecraft，只 kotlin stdlib + agent-api。
// 放 HttpTransport / JsonCodec 接口 + BeaconApiClient + 生命周期 + 快照 + applier + 退避 + settings + PlatformAdapter 接口。
plugins {
    kotlin("jvm")
}

dependencies {
    // 仅 Kotlin 标准库 + 只读 API 契约（agent-api 为纯 Java，可被 core 引用其类型）。
    implementation(kotlin("stdlib"))
    api(project(":agent-api"))

    // 单元测试：纯逻辑（退避、有效配置存储）。
    testImplementation(kotlin("test"))
}

// 启用 kotlin.test 的 JUnit 平台。
tasks.test {
    useJUnitPlatform()
}
