// agent-bungee：以 TabooLib 形式运行在 BungeeCord 代理的数据面插件，产出 BeaconAgentProxy.jar。
// M0 仅最小插件主类骨架，注册/心跳/长轮询等逻辑在 M5 实现。
plugins {
    kotlin("jvm")
    id("io.izzel.taboolib")
}

dependencies {
    // 依赖核心纯 Kotlin 模块（M5 在此承接 transport/codec 抽象）。
    compileOnly(project(":agent-core"))
    compileOnly(kotlin("stdlib"))
}

taboolib {
    // 插件元信息：主类与展示名（最终 jar 名为 BeaconAgentProxy）。
    description {
        name = "BeaconAgentProxy"
        contributors {
            name("Beacon")
        }
    }
    // 锁定 TabooLib 版本。
    version { taboolib = "6.2.3" }
    env {
        // 仅安装 BungeeCord 平台模块，M0 不引入配置/命令等其他模块。
        install("platform-bungee")
    }
    // 重定位 taboolib 包，避免与同代理上其它 TabooLib 插件冲突。
    relocate("taboolib", "${project.group}.taboolib")
}

// 产出 jar 基础名固定为 BeaconAgentProxy。
tasks.jar {
    archiveBaseName.set("BeaconAgentProxy")
}
