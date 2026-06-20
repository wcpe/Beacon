// agent-e2e：M6 端到端验收用的 TabooLib Bukkit 插件，产出 BeaconE2E.jar。
// 作为「业务插件」compileOnly 依赖 agent-api，经只读 API 读取约定 dataId 并把观测写标记文件。
// 用 jpenilla run-task 的 run-paper 插件提供的 runServer 任务自动下载并运行 Paper，
// 再把 BeaconAgent.jar 与本插件 jar 作为插件加载（Paper -add-plugin）后启动服务端，供整条 E2E 编排驱动。
import xyz.jpenilla.runpaper.task.RunServer

plugins {
    kotlin("jvm")
    id("io.izzel.taboolib")
    // run-paper：提供 runServer 任务，自动下载并运行 Paper 服务端（取代手写下载/启动任务）。
    id("xyz.jpenilla.run-paper")
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
        // 硬依赖 BeaconAgent：本验收插件运行期需经 BeaconAgent 暴露的 top.wcpe.beacon.agent.api.* 只读 API
        // 读有效配置。声明 depend 后 Bukkit 先加载 BeaconAgent，并让本插件 ClassLoader 可解析其 API 类，
        // 从而共享 BeaconAgentProvider 静态门面（否则类不可见，@Awake ENABLE 静默不执行）。
        dependencies {
            name("BeaconAgent").optional(false)
        }
    }
    // 与 agent 保持一致的 TabooLib 版本。
    version { taboolib = "6.2.3" }
    env {
        // Bukkit 平台即可：本插件只读 API + 写文件，不依赖配置 / 数据库等其它模块。
        install("platform-bukkit")
    }
    // TabooLib 按 project.group 自动把 taboolib 重定位到 top.wcpe.beacon.e2e.taboolib，
    // 与 BeaconAgent 的 top.wcpe.beacon.agent.taboolib 区分，避免同服冲突。
}

// 产出 jar 基础名固定为 BeaconE2E（TabooLib 的 description.name 只决定展示名，不改 jar 文件名）。
tasks.jar {
    archiveBaseName.set("BeaconE2E")
}

// ---------------------------------------------------------------------------
// runServer：由 run-paper 自动下载 Paper + 部署插件 + 启动服务端（M6 端到端编排入口）。
// 下载与启动交给 run-paper 的 runServer 任务，这里只做 Beacon 侧编排：投放数据面插件 jar、
// 经环境变量注入 agent 接入信息（FR-33）、写 TabooLib env.properties 与 Paper eula.txt。
// ---------------------------------------------------------------------------

// 运行根目录（落 .tmp，不入库）。
val runDir = rootProject.projectDir.resolve("../.tmp/e2e-run/bukkit")
// Paper 版本（Java 21 可运行）。可经 -Pe2ePaperVersion 覆盖（run-paper 解析最新构建，取代旧的 -Pe2ePaperUrl 直链）。
val paperVer = (project.findProperty("e2ePaperVersion") as String?) ?: "1.20.4"
// MC 监听端口：默认 25566，避让本机可能被其它 MC 服占用的 25565；经 -Pe2eMcPort 覆盖。
val mcPort = (project.findProperty("e2eMcPort") as String?) ?: "25566"
// agent 本地受限命令白名单（逗号分隔首 token，注入 BEACON_AGENT_OVERRIDE_COMMAND_WHITELIST）；
// 默认空 = 命令派发能力关闭（ADR-0011 默认 inert）；经 -Pe2eCommandWhitelist 覆盖，如 "beacone2ereload"。
val commandWhitelist = (project.findProperty("e2eCommandWhitelist") as String?) ?: ""
// 控制面地址默认指向 http://localhost:8848，可经 -Pe2eBeaconEndpoint 覆盖。
val beaconEndpoint = (project.findProperty("e2eBeaconEndpoint") as String?) ?: "http://localhost:8848"
// 共享令牌（X-Beacon-Token，须与控制面 agent-token 一致）；经 -Pe2eBootstrapToken 覆盖。
val bootstrapToken = (project.findProperty("e2eBootstrapToken") as String?) ?: "beacon-bootstrap-2026"
// 本机唯一身份，环境内唯一；经 -Pe2eServerId 覆盖。
val serverId = (project.findProperty("e2eServerId") as String?) ?: "e2e-bukkit-1"
// 环境（须与控制面 namespace 一致）；经 -Pe2eNamespace 覆盖。
val namespace = (project.findProperty("e2eNamespace") as String?) ?: "prod"
// TabooLib 6.2.3 的 repo-reflex 默认指向已下线的 sacredcraft.cn:8081，统一改指可达仓库；经 -Pe2eTabooRepo 覆盖。
val tabooRepo = (project.findProperty("e2eTabooRepo") as String?) ?: "https://repo.tabooproject.org/repository/releases"
// -Pe2eDebug 打开 TabooLib 调试输出，排查插件生命周期问题。
val e2eDebug = project.hasProperty("e2eDebug")
// 本模块版本（jar 名用，源自仓库根 VERSION）。
val beaconVer = project.version.toString()
// 数据面 agent（BeaconAgent）最终 jar：agent-bukkit 的 TabooLib 重定位产物。
val agentJar = project(":agent-bukkit").layout.buildDirectory.file("libs/BeaconAgent-$beaconVer.jar")
// 本验收插件最终 jar：TabooLib 重定位产物。
val e2eJar = layout.buildDirectory.file("libs/BeaconE2E-$beaconVer.jar")

// 关闭 run-paper 的「自动探测插件 jar」：默认会取标准 jar 任务产物，而 TabooLib 需要重定位后的 jar，
// 统一改由下方 pluginJars 显式投放（含数据面 agent jar）。
runPaper {
    disablePluginJarDetection()
}

// 配置 run-paper 默认的 runServer 任务（任务名 runServer 与 Go E2E 驱动约定一致，不改名）。
tasks.named<RunServer>("runServer") {
    group = "beacon-e2e"
    description = "由 run-paper 下载并启动 Paper，部署 BeaconAgent + E2E 插件（端到端验收）"

    // 指定 MC 版本，run-paper 解析最新构建并下载（取代硬编码直链）。
    minecraftVersion(paperVer)
    // 运行目录（落 .tmp）。
    runDirectory(runDir)

    // 先构建数据面 agent 与本插件的最终（已 relocate）jar，再启动。
    dependsOn(":agent-bukkit:build")
    dependsOn("build")

    // 显式投放重定位后的插件 jar（Paper >= 1.16.5 经 -add-plugin 加载，不入 plugins 目录）。
    pluginJars(e2eJar, agentJar)

    // 内存上限适中，避免占满宿主；file.encoding 由 run-task 统一设为 UTF-8。
    jvmArgs("-Xms512M", "-Xmx1536M")
    if (e2eDebug) {
        systemProperty("taboolib.debug", true)
    }
    // 指定监听端口（--nogui 由 run-paper 对 MC>=1.15 自动追加）。
    args("--port", mcPort)

    // FR-33：经环境变量注入 agent 接入信息（取代写 config.yml）。agent 的 EnvOverridingConfigReader
    // 以 BEACON_AGENT_<点分路径大写> 覆盖出厂 config.yml；其余字段走出厂默认。
    environment("BEACON_AGENT_BEACON_ENDPOINTS", beaconEndpoint)
    environment("BEACON_AGENT_BEACON_BOOTSTRAP_TOKEN", bootstrapToken)
    environment("BEACON_AGENT_IDENTITY_NAMESPACE", namespace)
    environment("BEACON_AGENT_IDENTITY_SERVER_ID", serverId)
    environment("BEACON_AGENT_IDENTITY_ADDRESS", "127.0.0.1:$mcPort")
    // 受限命令白名单（FR-15）：逗号分隔；空则不注入（出厂默认空 = 命令派发关闭）。
    if (commandWhitelist.isNotEmpty()) {
        environment("BEACON_AGENT_OVERRIDE_COMMAND_WHITELIST", commandWhitelist)
    }

    // 启动前置：写 Paper EULA 与 TabooLib 仓库覆盖 env.properties（agent 配置改由上面的环境变量注入）。
    doFirst {
        runDir.mkdirs()
        // Paper 需同意 EULA 方可启动（run-task 不代写，这里显式写入）。
        runDir.resolve("eula.txt").writeText("eula=true\n", Charsets.UTF_8)

        // 写入 TabooLib 运行期仓库覆盖 env.properties（置于工作目录，覆盖 jar 内 env）。
        // 两个插件（BeaconAgent / BeaconE2E）共用，一处生效。
        runDir.resolve("env.properties").writeText(
            """
            # 由 runServer 任务生成：覆盖 TabooLib 运行期库下载仓库（避开已下线的 sacredcraft.cn）。
            repo-central=https://maven.aliyun.com/repository/central
            repo-taboolib=$tabooRepo
            repo-reflex=$tabooRepo
            """.trimIndent() + "\n",
            Charsets.UTF_8,
        )

        logger.lifecycle("E2E 运行目录：${runDir.absolutePath}")
        logger.lifecycle("控制面地址：$beaconEndpoint，serverId=$serverId，namespace=$namespace（agent 配置经 BEACON_AGENT_* 注入）")
    }
}
