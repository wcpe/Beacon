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
    // Bukkit 平台 API（org.bukkit.*）：TabooLib 的 install("platform-bukkit") 只装平台实现，
    // 不会把 Bukkit API 传到编译类路径；FR-274 回退探针需要订阅聊天/进服事件，故显式 compileOnly。
    // 采与 agent-bungee 同款做法（见其 build.gradle.kts 对 bungeecord-api 的处理）。
    // 选 1.16.5 而非 1.20.x：本模块编译目标是 JVM 8（见下方 jvmTarget），
    // 而 paper-api 1.20.4 要求 JVM 17+，会被 Gradle 的兼容性校验直接拒绝。
    // 探针只用到 Bukkit 最基础的 API（事件订阅 / shutdown / chat），1.16.5 的位置与签名在 1.20 上同样成立。
    compileOnly("org.spigotmc:spigot-api:1.16.5-R0.1-SNAPSHOT")
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
    version { taboolib = "6.3.0-wcpe.5" }
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
// FR-274 回退拓扑：需要两台大厅（分属两个小区）与一台可关闭的业务子服。
// 端口各自独立，避免与既有单后端 serve 争用；可用 Gradle 属性覆盖以避本地端口冲突。
val lobby1Port = integerProperty("e2eLobby1Port", 25568)
val lobby2Port = integerProperty("e2eLobby2Port", 25569)
val gamePort = integerProperty("e2eGamePort", 25570)
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
        // 服务发现 / 拓扑订阅 / 名册探针（对应 agent-api 的 Discovery）。
        optionalEnvironment("BEACON_E2E_DISCOVERY")?.let { env("BEACON_E2E_DISCOVERY", it) }
        optionalEnvironment("BEACON_E2E_DISCOVERY_GROUP")?.let { env("BEACON_E2E_DISCOVERY_GROUP", it) }
        // 节点自声明探针（agent-api 唯一写入面）。
        optionalEnvironment("BEACON_E2E_DECLARATION")?.let { env("BEACON_E2E_DECLARATION", it) }
        optionalEnvironment("BEACON_E2E_DECL_LABEL_KEY")?.let { env("BEACON_E2E_DECL_LABEL_KEY", it) }
        // 健康视图 + 准入作用域探针。
        optionalEnvironment("BEACON_E2E_HEALTH_ADMISSION")?.let { env("BEACON_E2E_HEALTH_ADMISSION", it) }
        // 前缀过滤订阅探针。
        optionalEnvironment("BEACON_E2E_PREFIX_SUB")?.let { env("BEACON_E2E_PREFIX_SUB", it) }
        optionalEnvironment("BEACON_E2E_PREFIX_LIST")?.let { env("BEACON_E2E_PREFIX_LIST", it) }
        // 门面连通性探针（connected / effectiveMd5 / awaitRegistered）。
        optionalEnvironment("BEACON_E2E_FACADE")?.let { env("BEACON_E2E_FACADE", it) }
        // FR-276 join 代执行探针：外部驱动投递请求文件，探针在主线程经控制台派发 /beacon join。
        // 必须显式门控：该探针具备「派发控制台命令」的能力，不经开关不得上线。
        optionalEnvironment("BEACON_E2E_JOIN")?.let { env("BEACON_E2E_JOIN", it) }
    }
    proxy("bungee") {
        platform = bungeecord
        port = proxyPort
        // 必须覆盖全部后端：mc-testkit 在配置期校验「多后端场景的代理须 routesTo 每个被引用后端」，
        // 漏一个会直接报错（正是这条校验提前拦住了回退场景拓扑写漏）。
        routesTo("backend", "lobby1", "lobby2", "game")
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
    // ── FR-274 子服关闭回退拓扑 ──────────────────────────────────────────
    // 两个大厅分属一区 / 二区，一台业务子服可被关闭以触发回退；全部经同一代理转发。
    // **顺序有意义**：代理的 priorities 按声明顺序取首个可用后端，故 game 必须排最前——
    // bot 要先落在业务子服里，才能把这个子服关掉去验证回退。
    backend("game") {
        platform = paper
        version = paperVersion
        port = gamePort
        templateDirectory(backendTemplate)
        env("JAVA_TOOL_OPTIONS", "-Xms512M -Xmx1536M -Dtaboolib.debug=true")
        env("BEACON_E2E_RECONNECT_ROLE", "game")
    }
    backend("lobby1") {
        platform = paper
        version = paperVersion
        port = lobby1Port
        templateDirectory(backendTemplate)
        env("JAVA_TOOL_OPTIONS", "-Xms512M -Xmx1536M -Dtaboolib.debug=true")
        env("BEACON_E2E_RECONNECT_ROLE", "lobby-1")
    }
    backend("lobby2") {
        platform = paper
        version = paperVersion
        port = lobby2Port
        templateDirectory(backendTemplate)
        env("JAVA_TOOL_OPTIONS", "-Xms512M -Xmx1536M -Dtaboolib.debug=true")
        env("BEACON_E2E_RECONNECT_ROLE", "lobby-2")
    }
    // 回退场景：bot 进 game 后端 → 触发其关闭 → agent 把玩家送回同区大厅。
    // backends(...) 的顺序同样须让 game 在首位（与上面声明顺序保持一致）。
    scenario("reconnect") {
        backends("game", "lobby1", "lobby2")
        via = "bungee"
        bot {
            username = "Reconnector"
            action = "reconnect-fallback"
        }
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
