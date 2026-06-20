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
        // jpenilla run-task：为 e2e 模块提供 runServer(Paper)/runWaterfall(Waterfall) 自动下载并运行
        // MC 服务端/代理的任务，取代手写的 PrepareMinecraftServerEnvTask 下载 + JavaExec 启动。
        // 锁定 2.3.1：run-task 3.0.x 起在发布元数据声明 plugin-api-version=8.14.3、要求 Gradle ≥ 8.14.3，
        // 而本构建用 Gradle 8.5；2.3.1 是最后一个不带该门槛、兼容 8.5 的版本（run-paper/run-waterfall API 与 3.0.x 一致）。
        id("xyz.jpenilla.run-paper") version "2.3.1"
        id("xyz.jpenilla.run-waterfall") version "2.3.1"
    }
}

rootProject.name = "beacon-agent"

// 子模块（依赖方向无环：bukkit/bungee → {core, adapters, api, kit}；adapters → core → api；kit → api）：
// agent-api      纯 Java 8 只读契约，业务插件 compileOnly 依赖
// agent-kit      纯 Java 8 便捷接入层（零三方依赖、只依赖 agent-api），下游可选依赖以收口接入样板
// agent-core     平台无关核心（零具体库依赖：只 kotlin stdlib + agent-api）
// agent-adapters OkHttp + kotlinx.serialization 适配器（唯一碰具体库的模块）
// agent-bukkit   Bukkit 子服插件壳，产出 BeaconAgent.jar
// agent-bungee   BungeeCord 代理插件壳，产出 BeaconAgentProxy.jar
// agent-e2e       M6 端到端验收用 TabooLib Bukkit 业务插件（compileOnly agent-api），并提供 runServer 任务
// agent-e2e-bungee 同上的 BungeeCord 验收插件，并提供 runBungee 任务
include("agent-api")
include("agent-kit")
include("agent-core")
include("agent-adapters")
include("agent-bukkit")
include("agent-bungee")
include("agent-e2e")
include("agent-e2e-bungee")
