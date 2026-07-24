// Beacon 数据面 agent 的 Gradle 多模块构建定义（与控制面 Go 工程相互独立）。

// 集中声明插件版本（pluginManagement），子模块 apply 时不再带版本号，避免在根 plugins{}
// 同时声明多个 Kotlin 插件触发的 projectsEvaluated 评估期冲突。
pluginManagement {
    val mcTestkitIncludeBuild = System.getenv("MC_TESTKIT_INCLUDE_BUILD").orEmpty().trim()
    if (mcTestkitIncludeBuild.isNotEmpty()) {
        // 仅联调未发布版本时替换插件解析；默认仍消费 maven.wcpe.top 的正式工件。
        includeBuild(mcTestkitIncludeBuild)
    }
    repositories {
        gradlePluginPortal()
        mavenCentral()
        // mc-testkit 0.5.0 与其它 WCPE Gradle 插件的正式解析仓库。
        maven("https://maven.wcpe.top/repository/maven-public/")
        // TabooLib 官方发布仓库（解析 io.izzel.taboolib gradle 插件）。
        maven("https://repo.tabooproject.org/repository/releases")
    }
    plugins {
        kotlin("jvm") version "1.9.22"
        kotlin("plugin.serialization") version "1.9.22"
        id("io.izzel.taboolib") version "2.0.37"
        // ktlint Gradle 插件：格式化与格式检查（与 Gradle 8.5 / Kotlin 1.9.x 兼容的固定版本）。
        id("org.jlleitschuh.gradle.ktlint") version "12.1.1"
        // detekt 官方静态检查插件：结构 / 复杂度 / 坏味道检查（兼容 Kotlin 1.9.x 的固定版本）。
        id("io.gitlab.arturbosch.detekt") version "1.23.6"
        // 真实 Paper/BungeeCord E2E 的统一拓扑编排插件。
        id("top.wcpe.mc-testkit") version "0.5.0"
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
// agent-e2e       M6 端到端验收用 TabooLib Bukkit 业务插件，并统一声明 mc-testkit serve 拓扑
// agent-e2e-bungee 同上的 BungeeCord 验收插件，由 agent-e2e 的代理节点注入
include("agent-api")
include("agent-kit")
include("agent-core")
include("agent-adapters")
include("agent-bukkit")
include("agent-bungee")
include("agent-e2e")
include("agent-e2e-bungee")
