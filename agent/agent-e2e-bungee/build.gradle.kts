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
// 下载与启动交给 run-task，这里只做 Beacon 侧编排：投放数据面插件 jar、生成 agent config.yml / env.properties。
// ---------------------------------------------------------------------------

// 运行根目录（落 .tmp，不入库）。
val runDir = rootProject.projectDir.resolve("../.tmp/e2e-run/bungee")
// Waterfall 版本（Java 21 可运行）。可经 -Pe2eWaterfallVersion 覆盖（run-waterfall 解析最新构建，取代旧的 -Pe2eWaterfallUrl 直链）。
val waterfallVer = (project.findProperty("e2eWaterfallVersion") as String?) ?: "1.20"
// 控制面地址默认指向 http://localhost:8848，可经 -Pe2eBeaconEndpoint 覆盖。
val beaconEndpoint = (project.findProperty("e2eBeaconEndpoint") as String?) ?: "http://localhost:8848"
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

    // 启动前置：生成 agent config.yml 与 TabooLib 仓库覆盖 env.properties（代理无需 EULA）。
    doFirst {
        runDir.mkdirs()
        // 放置 agent 的 config.yml 到其数据目录（plugins/BeaconAgentProxy/）。
        val agentDataDir = runDir.resolve("plugins/BeaconAgentProxy")
        agentDataDir.mkdirs()
        agentDataDir.resolve("config.yml").writeText(
            agentConfigYaml(beaconEndpoint, serverId, namespace),
            Charsets.UTF_8,
        )

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
        logger.lifecycle("控制面地址：$beaconEndpoint，serverId=$serverId，namespace=$namespace")
    }
}

/** 生成 E2E 用 agent config.yml；无 zone、无 canary，仅最小接入信息。代理端口默认 25577。 */
fun agentConfigYaml(endpoint: String, serverId: String, namespace: String): String = """
    # Beacon agent E2E 运行配置（由 runBungee 任务生成，仅供端到端验收）。
    beacon:
      # 控制面地址：指向本地起的 Beacon（默认 8848）。
      endpoints:
        - "$endpoint"
      # 共享令牌，需与控制面 agent-token 一致。
      bootstrap-token: "beacon-bootstrap-2026"

    identity:
      # 环境：必须与控制面 namespace 一致。
      namespace: "$namespace"
      # 本机唯一身份，环境内唯一。
      server-id: "$serverId"
      # 大区提示：尚未指派 zone 时作兜底 group。
      group-hint: "area1"
      # 对外可达地址 ip:port（代理默认 25577）。
      address: "127.0.0.1:25577"
      # 业务版本标签。
      version: "1.0.0"
      # 容量（发现过滤维度）。
      capacity: 1000
      # 权重（发现过滤维度）。
      weight: 100
      # 自定义注册元数据。
      metadata:
        region: "cn-east"

    timing:
      # 长轮询客户端期望挂起上限（毫秒）。
      poll-timeout-ms: 30000
      # 普通请求连接与读超时（毫秒）。
      request-timeout-ms: 5000
      # 心跳周期兜底值（毫秒）。
      heartbeat-fallback-ms: 10000

    backoff:
      # 指数退避初始等待（毫秒）。
      initial-ms: 1000
      # 指数退避等待上限（毫秒）。
      max-ms: 30000
      # 每次退避的倍率。
      multiplier: 2.0
      # 退避抖动比例（±）。
      jitter-ratio: 0.2

    snapshot:
      # 启用本地快照 fail-static。
      enabled: true
      # 快照文件名。
      file-name: "effective-config.snapshot.json"
""".trimIndent() + "\n"
