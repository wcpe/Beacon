// agent-e2e-bungee：M6 端到端验收用的 TabooLib BungeeCord 插件，产出 BeaconE2EProxy.jar。
// 作为「业务插件」compileOnly 依赖 agent-api，经只读 API 读取约定 dataId 并把观测写标记文件。
// 用 jpenilla run-task 的 run-waterfall 任务类型自定义命名为 runBungee：自动下载并运行 Waterfall，
// 再把 BeaconAgentProxy.jar 与本插件 jar 投入 plugins 目录后启动代理（代理无需 EULA）。
import xyz.jpenilla.runwaterfall.task.RunWaterfall

plugins {
    kotlin("jvm")
    id("io.izzel.taboolib")
    // run-waterfall：提供 RunWaterfall 任务类型，自动下载并运行 Waterfall 代理（取代手写下载/启动任务）。
    id("xyz.jpenilla.run-waterfall")
}

// 单独的 group：与 BeaconAgentProxy（top.wcpe.beacon.agent）区分 relocate 根包，避免同代理冲突。
group = "top.wcpe.beacon.e2e"

// 先让 agent-api 完成评估，再进入本壳 afterEvaluate，避免评估锁定期 apply 冲突。
evaluationDependsOn(":agent-api")

dependencies {
    // 仅编译期依赖对外只读 API（模拟真实业务插件；运行期由 BeaconAgentProxy 提供）。
    compileOnly(project(":agent-api"))
    compileOnly(kotlin("stdlib"))
    // BungeeCord 平台 API（net.md_5.bungee.*）：FR-4 目录探针需读 ProxyServer 服务器目录与命令注册状态；
    // TabooLib install(platform-bungee) 未把它加到编译类路径，显式 compileOnly 引入（运行期由代理提供）。
    compileOnly("net.md-5:bungeecord-api:1.20-R0.2@jar")
}

taboolib {
    description {
        name = "BeaconE2EProxy"
        contributors {
            name("Beacon")
        }
        // 硬依赖 BeaconAgentProxy：运行期需经其暴露的 top.wcpe.beacon.agent.api.* 只读 API 读有效配置。
        dependencies {
            name("BeaconAgentProxy").optional(false)
        }
    }
    version { taboolib = "6.2.3" }
    env {
        // BungeeCord 平台。
        install("platform-bungee")
    }
    // TabooLib 按 project.group 自动把 taboolib 重定位到 top.wcpe.beacon.e2e.taboolib。
}

// 产出 jar 基础名固定为 BeaconE2EProxy。
tasks.jar {
    archiveBaseName.set("BeaconE2EProxy")
}

// ---------------------------------------------------------------------------
// runBungee：由 run-waterfall 自动下载 Waterfall + 部署插件 + 启动代理（M6 端到端编排入口，代理侧）。
// 用 run-waterfall 的 RunWaterfall 任务类型自定义命名为 runBungee（与 Go E2E 驱动约定一致）；
// 下载与启动交给 run-task，这里只做 Beacon 侧编排：投放数据面插件 jar、经环境变量注入 agent 接入信息（FR-33）。
// ---------------------------------------------------------------------------

// 运行根目录（落 .tmp，不入库）。
val runDir = rootProject.projectDir.resolve("../.tmp/e2e-run/bungee")
// Waterfall 版本（Java 21 可运行）。可经 -Pe2eWaterfallVersion 覆盖（run-waterfall 解析最新构建，取代旧的 -Pe2eWaterfallUrl 直链）。
val waterfallVer = (project.findProperty("e2eWaterfallVersion") as String?) ?: "1.20"
// 控制面地址默认指向 http://localhost:8848，可经 -Pe2eBeaconEndpoint 覆盖。
val beaconEndpoint = (project.findProperty("e2eBeaconEndpoint") as String?) ?: "http://localhost:8848"
// 共享令牌（X-Beacon-Token，须与控制面 agent-token 一致）；经 -Pe2eBootstrapToken 覆盖。
val bootstrapToken = (project.findProperty("e2eBootstrapToken") as String?) ?: "beacon-bootstrap-2026"
// 本机唯一身份，环境内唯一；经 -Pe2eServerId 覆盖。
val serverId = (project.findProperty("e2eServerId") as String?) ?: "e2e-bungee-1"
// 环境（须与控制面 namespace 一致）；经 -Pe2eNamespace 覆盖。
val namespace = (project.findProperty("e2eNamespace") as String?) ?: "prod"
// TabooLib 6.2.3 的 repo-reflex 默认指向已下线的 sacredcraft.cn:8081，统一改指可达仓库；经 -Pe2eTabooRepo 覆盖。
val tabooRepo = (project.findProperty("e2eTabooRepo") as String?) ?: "https://repo.tabooproject.org/repository/releases"
// 本模块版本（jar 名用，源自仓库根 VERSION）。
val beaconVer = project.version.toString()
// 数据面 agent（BeaconAgentProxy）最终 jar：agent-bungee 的 TabooLib 重定位产物。
val agentJar = project(":agent-bungee").layout.buildDirectory.file("libs/BeaconAgentProxy-$beaconVer.jar")
// 本验收插件最终 jar：TabooLib 重定位产物。
val e2eJar = layout.buildDirectory.file("libs/BeaconE2EProxy-$beaconVer.jar")

// 自定义命名的 RunWaterfall 任务（run-waterfall 的自动探测只作用于其默认 runWaterfall 任务，
// 故这里需显式 pluginJars 投放重定位后的 jar；run-waterfall 会把它们复制进 plugins/ 加载）。
val runBungee by tasks.registering(RunWaterfall::class) {
    group = "beacon-e2e"
    description = "由 run-waterfall 下载并启动 Waterfall，部署 BeaconAgentProxy + E2E 代理插件（端到端验收，代理侧）"

    // 指定 Waterfall 版本，run-waterfall 解析最新构建并下载（取代硬编码直链）。
    waterfallVersion(waterfallVer)
    // 运行目录（落 .tmp）。
    runDirectory(runDir)

    // 先构建数据面 agent 与本插件的最终（已 relocate）jar，再启动。
    dependsOn(":agent-bungee:build")
    dependsOn("build")

    // 显式投放重定位后的插件 jar（run-waterfall 复制进 plugins/）。
    pluginJars(e2eJar, agentJar)

    // 内存上限适中，避免占满宿主；file.encoding 由 run-task 统一设为 UTF-8。
    jvmArgs("-Xms256M", "-Xmx768M")

    // FR-33：经环境变量注入 agent 接入信息（取代写 config.yml）。agent 的 EnvOverridingConfigReader
    // 以 BEACON_AGENT_<点分路径大写> 覆盖出厂 config.yml；其余字段走出厂默认。代理对外端口固定 25577。
    environment("BEACON_AGENT_BEACON_ENDPOINTS", beaconEndpoint)
    environment("BEACON_AGENT_BEACON_BOOTSTRAP_TOKEN", bootstrapToken)
    environment("BEACON_AGENT_IDENTITY_NAMESPACE", namespace)
    environment("BEACON_AGENT_IDENTITY_SERVER_ID", serverId)
    environment("BEACON_AGENT_IDENTITY_ADDRESS", "127.0.0.1:25577")

    // 启动前置：写 TabooLib 仓库覆盖 env.properties（代理无需 EULA；agent 配置改由上面的环境变量注入）。
    doFirst {
        runDir.mkdirs()
        // TabooLib 运行期仓库覆盖（避开已下线的 sacredcraft.cn）。
        runDir.resolve("env.properties").writeText(
            """
            repo-central=https://maven.aliyun.com/repository/central
            repo-taboolib=$tabooRepo
            repo-reflex=$tabooRepo
            """.trimIndent() + "\n",
            Charsets.UTF_8,
        )

        logger.lifecycle("E2E 代理运行目录：${runDir.absolutePath}")
        logger.lifecycle("控制面地址：$beaconEndpoint，serverId=$serverId，namespace=$namespace（agent 配置经 BEACON_AGENT_* 注入）")
    }
}
