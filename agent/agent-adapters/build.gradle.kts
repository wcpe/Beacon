// agent-adapters：唯一碰具体库的模块。实现 core 定义的 HttpTransport / JsonCodec 接口。
// OkHttpTransport（HTTP）+ KotlinxJsonCodec（JSON 泛型树 ↔ 文本）。
plugins {
    kotlin("jvm")
    // kotlinx.serialization 编译器插件（仅本模块需要）。
    kotlin("plugin.serialization")
}

dependencies {
    implementation(kotlin("stdlib"))
    // 依赖核心接口与数据类型（HttpTransport/JsonCodec/HttpRequest...）。
    api(project(":agent-core"))

    // HTTP 客户端：长轮询读超时控制直接、连接复用稳。
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
    // JSON 序列化：以 JsonElement 做 Map↔json 转换实现 JsonCodec。
    implementation("org.jetbrains.kotlinx:kotlinx-serialization-json:1.6.3")

    // 测试：BeaconApiClient / SnapshotStore / ConfigApplier 用真实 KotlinxJsonCodec + 假 HttpTransport。
    testImplementation(kotlin("test"))
    testImplementation(project(":agent-core"))
}

tasks.test {
    useJUnitPlatform()
}
