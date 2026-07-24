// agent-e2e-bungee：M6 端到端验收用的 TabooLib BungeeCord 插件，产出 BeaconE2EProxy.jar。
// 作为业务插件 compileOnly 依赖 agent-api，由 agent-e2e 的 mc-testkit 原生 BungeeCord 节点负责注入与启动。
plugins {
    kotlin("jvm")
    id("io.izzel.taboolib")
}

// 单独的 group：与 BeaconAgentProxy（top.wcpe.beacon.agent）区分 relocate 根包，避免同代理冲突。
group = "top.wcpe.beacon.e2e"

// 先让 agent-api 完成评估，再进入本壳 afterEvaluate，避免评估锁定期 apply 冲突。
evaluationDependsOn(":agent-api")

dependencies {
    // 仅编译期依赖对外只读 API（模拟真实业务插件；运行期由 BeaconAgentProxy 提供）。
    compileOnly(project(":agent-api"))
    compileOnly(kotlin("stdlib"))
    // BungeeCord 平台 API：目录探针需读 ProxyServer 服务器目录与命令注册状态。
    compileOnly("net.md-5:bungeecord-api:1.20-R0.2@jar")
    // 连接采集探针需引用 agent 已装配的真采集入口；仅编译期依赖，运行期由 BeaconAgentProxy 提供。
    compileOnly(project(":agent-bungee"))
    compileOnly(project(":agent-core"))
}

taboolib {
    description {
        name = "BeaconE2EProxy"
        contributors {
            name("Beacon")
        }
        // 硬依赖 BeaconAgentProxy：运行期需经其暴露的只读 API 读取有效配置。
        dependencies {
            name("BeaconAgentProxy").optional(false)
        }
    }
    version { taboolib = "6.2.3" }
    env {
        // BungeeCord 平台。
        install("platform-bungee")
    }
}

// 产出 jar 基础名固定为 BeaconE2EProxy。
tasks.jar {
    archiveBaseName.set("BeaconE2EProxy")
}
