// agent-e2e：M6 端到端验收用的 TabooLib Bukkit 插件，产出 BeaconE2E.jar。
// 本模块同时用 mc-testkit 0.5.0 声明一个 Paper 后端与一个原生 BungeeCord 代理，统一提供三个持久 serve 入口。
plugins {
    kotlin("jvm")
    id("io.izzel.taboolib")
    id("top.wcpe.mc-testkit")
}

// 单独的 group：TabooLib 按 project.group 推导 relocate 根包，本模块需与 BeaconAgent（top.wcpe.beacon.agent）
// 区分，否则两插件 relocate 到同一 top.wcpe.beacon.agent.taboolib 包、主类同名而在同服相互冲突。
group = "top.wcpe.beacon.e2e"

// 先让 agent-api 完成评估（应用其 java 插件），再进入本壳的 afterEvaluate，避免评估锁定期 apply 冲突。
evaluationDependsOn(":agent-api")

dependencies {
    // 仅编译期依赖对外只读 API：模拟真实业务插件的依赖方式（不把 API 打进 jar，运行期由 BeaconAgent 提供）。
    compileOnly(project(":agent-api"))
    compileOnly(kotlin("stdlib"))
}

taboolib {
    // 插件元信息：展示名（最终 jar 名为 BeaconE2E）。
    description {
        name = "BeaconE2E"
        contributors {
            name("Beacon")
        }
        // 硬依赖 BeaconAgent：本验收插件运行期需经 BeaconAgent 暴露的只读 API 读取有效配置。
        dependencies {
            name("BeaconAgent").optional(false)
        }
    }
    // 与现有 E2E 探针保持一致的 TabooLib 版本。
    version { taboolib = "6.2.3" }
    env {
        // Bukkit 平台即可：本插件只读 API + 写文件，不依赖配置 / 数据库等其它模块。
        install("platform-bukkit")
    }
}

// 产出 jar 基础名固定为 BeaconE2E。
tasks.jar {
    archiveBaseName.set("BeaconE2E")
}

fun optionalEnvironment(name: String): String? = providers.environmentVariable(name).orNull?.takeIf { it.isNotBlank() }

fun integerProperty(
    name: String,
    fallback: Int,
): Int {
    val raw = providers.gradleProperty(name).orNull ?: fallback.toString()
    return raw.toIntOrNull() ?: error("Gradle 属性 $name 必须是整数，实际为：$raw")
}

val paperVersion = providers.gradleProperty("e2ePaperVersion").orElse("1.20.4").get()
val backendPort = integerProperty("e2eMcPort", 25566)
val proxyPort = integerProperty("e2eProxyPort", 25577)
val beaconVersion = project.version.toString()

// Q2 harness 只把非敏感端口放入 -P；端点、令牌、namespace 与身份全部从子进程环境读取。
val beaconEndpoint = optionalEnvironment("BEACON_AGENT_BEACON_ENDPOINTS")
val bootstrapToken = optionalEnvironment("BEACON_AGENT_BEACON_BOOTSTRAP_TOKEN")
val namespace = optionalEnvironment("BEACON_AGENT_IDENTITY_NAMESPACE")
val sharedServerId = optionalEnvironment("BEACON_AGENT_IDENTITY_SERVER_ID")
val sharedAddress = optionalEnvironment("BEACON_AGENT_IDENTITY_ADDRESS")
val connectionProbe = optionalEnvironment("BEACON_E2E_CONNINJECT")

// serveDirectory 使用节点专属键；单节点 serve 使用通用 BEACON_AGENT_IDENTITY_* 键。
// serveProxy 仍需一个 mc-testkit 后端作为合法拓扑，因此连接探针场景从 Q2 通用身份派生伴随后端身份，避免与代理冲突。
val backendServerId =
    optionalEnvironment("BEACON_E2E_BACKEND_SERVER_ID")
        ?: sharedServerId?.let { if (connectionProbe != null) "$it-backend" else it }
val backendAddress =
    optionalEnvironment("BEACON_E2E_BACKEND_ADDRESS")
        ?: sharedAddress?.let {
            if (connectionProbe != null) "${it.substringBeforeLast(':')}:$backendPort" else it
        }
val proxyServerId = optionalEnvironment("BEACON_E2E_PROXY_SERVER_ID") ?: sharedServerId
val proxyAddress = optionalEnvironment("BEACON_E2E_PROXY_ADDRESS") ?: sharedAddress

val backendAgentJar =
    project(":agent-bukkit").layout.buildDirectory
        .file("libs/BeaconAgent-$beaconVersion.jar").get().asFile.absolutePath
val backendE2eJar =
    layout.buildDirectory
        .file("libs/BeaconE2E-$beaconVersion.jar").get().asFile.absolutePath
val proxyAgentJar =
    project(":agent-bungee").layout.buildDirectory
        .file("libs/BeaconAgentProxy-$beaconVersion.jar").get().asFile.absolutePath
val proxyE2eJar =
    project(":agent-e2e-bungee").layout.buildDirectory
        .file("libs/BeaconE2EProxy-$beaconVersion.jar").get().asFile.absolutePath
val backendTemplate = layout.projectDirectory.dir("src/e2e-templates/backend").asFile.absolutePath
val proxyTemplate = layout.projectDirectory.dir("src/e2e-templates/proxy").asFile.absolutePath

mcTestkit {
    backend("backend") {
        platform = paper
        version = paperVersion
        port = backendPort
        templateDirectory(backendTemplate)
        env("JAVA_TOOL_OPTIONS", "-Xms512M -Xmx1536M -Dtaboolib.debug=true")
        beaconEndpoint?.let { env("BEACON_AGENT_BEACON_ENDPOINTS", it) }
        bootstrapToken?.let { env("BEACON_AGENT_BEACON_BOOTSTRAP_TOKEN", it) }
        namespace?.let { env("BEACON_AGENT_IDENTITY_NAMESPACE", it) }
        backendServerId?.let { env("BEACON_AGENT_IDENTITY_SERVER_ID", it) }
        backendAddress?.let { env("BEACON_AGENT_IDENTITY_ADDRESS", it) }
        optionalEnvironment("BEACON_AGENT_OVERRIDE_COMMAND_WHITELIST")?.let {
            env("BEACON_AGENT_OVERRIDE_COMMAND_WHITELIST", it)
        }
        optionalEnvironment("BEACON_AGENT_MESSAGING_ENABLED")?.let {
            env("BEACON_AGENT_MESSAGING_ENABLED", it)
        }
        optionalEnvironment("BEACON_E2E_MESSAGING")?.let { env("BEACON_E2E_MESSAGING", it) }
        optionalEnvironment("BEACON_E2E_SCHED_ZONE")?.let { env("BEACON_E2E_SCHED_ZONE", it) }
    }
    proxy("bungee") {
        platform = bungeecord
        port = proxyPort
        routesTo("backend")
        plugin(proxyAgentJar)
        plugin(proxyE2eJar)
        templateDirectory(proxyTemplate)
        env("JAVA_TOOL_OPTIONS", "-Xms256M -Xmx768M -Dtaboolib.debug=true")
        beaconEndpoint?.let { env("BEACON_AGENT_BEACON_ENDPOINTS", it) }
        bootstrapToken?.let { env("BEACON_AGENT_BEACON_BOOTSTRAP_TOKEN", it) }
        namespace?.let { env("BEACON_AGENT_IDENTITY_NAMESPACE", it) }
        proxyServerId?.let { env("BEACON_AGENT_IDENTITY_SERVER_ID", it) }
        proxyAddress?.let { env("BEACON_AGENT_IDENTITY_ADDRESS", it) }
        connectionProbe?.let { env("BEACON_E2E_CONNINJECT", it) }
    }
    serve("paper") {
        backend = "backend"
    }
    serve("directory") {
        backend = "backend"
        via = "bungee"
    }
    serve("proxy") {
        backend = "backend"
        via = "bungee"
    }
    dependencies {
        pluginUnderTest = backendAgentJar
        plugin(backendE2eJar)
    }
}

// mc-testkit 在 afterEvaluate 注册 serve 任务；matching.configureEach 可同时覆盖当前与后续注册的任务。
tasks.matching { it.name == "servePaper" }.configureEach {
    dependsOn(":agent-bukkit:build", ":agent-e2e:build")
}
tasks.matching { it.name == "serveDirectory" || it.name == "serveProxy" }.configureEach {
    dependsOn(
        ":agent-bukkit:build",
        ":agent-e2e:build",
        ":agent-bungee:build",
        ":agent-e2e-bungee:build",
    )
}
