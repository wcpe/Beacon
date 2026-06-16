// Beacon 数据面 agent 的 Gradle 多模块构建定义（与控制面 Go 工程相互独立）。

// 集中声明插件版本（pluginManagement），子模块 apply 时不再带版本号，避免在根 plugins{}
// 同时声明多个 Kotlin 插件触发的 projectsEvaluated 评估期冲突。
pluginManagement {
    repositories {
        gradlePluginPortal()
        mavenCentral()
        // TabooLib 官方发布仓库（解析 io.izzel.taboolib gradle 插件）。
        maven("https://repo.tabooproject.org/repository/releases")
    }
    plugins {
        kotlin("jvm") version "1.9.22"
        kotlin("plugin.serialization") version "1.9.22"
        id("io.izzel.taboolib") version "2.0.37"
    }
}

rootProject.name = "beacon-agent"

// 五个子模块（依赖方向无环：bukkit/bungee → {core, adapters, api}；adapters → core → api）：
// agent-api      纯 Java 8 只读契约，业务插件 compileOnly 依赖
// agent-core     平台无关核心（零具体库依赖：只 kotlin stdlib + agent-api）
// agent-adapters OkHttp + kotlinx.serialization 适配器（唯一碰具体库的模块）
// agent-bukkit   Bukkit 子服插件壳，产出 BeaconAgent.jar
// agent-bungee   BungeeCord 代理插件壳，产出 BeaconAgentProxy.jar
include("agent-api")
include("agent-core")
include("agent-adapters")
include("agent-bukkit")
include("agent-bungee")
