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

    // 第三方库仅编译期可见：运行期由壳的 @RuntimeDependencies 动态下载（不打包进 jar，参考 CoreLib）。
    // HTTP 客户端：长轮询读超时控制直接、连接复用稳。
    compileOnly("com.squareup.okhttp3:okhttp:4.12.0")
    // JSON 序列化：以 JsonElement 做 Map↔json 转换实现 JsonCodec。
    compileOnly("org.jetbrains.kotlinx:kotlinx-serialization-json:1.6.3")
    // Redis 客户端（FR-26 跨服消息中间件，ADR-0016 决策 14）：仅编译期可见，
    // 运行期由壳 @RuntimeDependencies 动态下载、relocate 到隔离命名空间，不打包、不经 CoreLib。
    compileOnly("redis.clients:jedis:4.2.3")

    // 测试：BeaconApiClient / SnapshotStore / ConfigApplier 用真实 KotlinxJsonCodec + 假 HttpTransport，
    // 测试期需真实 okhttp/kotlinx 在 classpath。
    testImplementation(kotlin("test"))
    testImplementation(project(":agent-core"))
    testImplementation("com.squareup.okhttp3:okhttp:4.12.0")
    testImplementation("org.jetbrains.kotlinx:kotlinx-serialization-json:1.6.3")
    // Redis 客户端：RedisChannels / RedisPlayerRoster 纯逻辑单测需 Jedis 类型在 classpath。
    testImplementation("redis.clients:jedis:4.2.3")
}

tasks.test {
    useJUnitPlatform()
}
