// agent-core：纯 Kotlin 库，不挂任何 Minecraft 平台，也不依赖 TabooLib。
// 后续（M5）在此放置 transport/codec 抽象（见 ADR-0005）；M0 仅占位。
plugins {
    kotlin("jvm")
}

dependencies {
    // 仅 Kotlin 标准库，禁止引入任何 Bukkit/Bungee/Minecraft 依赖。
    implementation(kotlin("stdlib"))
}
