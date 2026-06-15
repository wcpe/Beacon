// Beacon 数据面 agent 的 Gradle 多模块构建定义（与控制面 Go 工程相互独立）。
rootProject.name = "beacon-agent"

// 三个子模块：核心纯 Kotlin 库 + Bukkit 子服插件 + BungeeCord 代理插件。
include("agent-core")
include("agent-bukkit")
include("agent-bungee")
