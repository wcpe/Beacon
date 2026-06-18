// agent-e2e-bungee：M6 端到端验收用的 TabooLib BungeeCord 插件，产出 BeaconE2EProxy.jar。
// 作为「业务插件」compileOnly 依赖 agent-api，经只读 API 读取约定 dataId 并把观测写标记文件。
// 另注册 runBungee 任务：用 TabooLib 的 PrepareMinecraftServerEnvTask 自动下载 Waterfall、写 EULA（代理无害），
// 再把 BeaconAgentProxy.jar 与本插件 jar 放进 plugins 目录后启动代理。
import io.izzel.taboolib.gradle.PrepareMinecraftServerEnvTask

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
// runBungee：自动下载 Waterfall + 部署插件 + 启动代理（M6 端到端编排入口，代理侧）。
// ---------------------------------------------------------------------------

// 运行根目录（落 .tmp，不入库）。
val runDir = rootProject.projectDir.resolve("../.tmp/e2e-run/bungee")
// 代理 jar 文件名。
val proxyJarName = "waterfall.jar"
// Waterfall 1.20 指定构建直链（Java 21 可运行，PaperMC API 直链无重定位）。可经 -Pe2eWaterfallUrl 覆盖。
val waterfallUrl = (project.findProperty("e2eWaterfallUrl") as String?)
    ?: "https://api.papermc.io/v2/projects/waterfall/versions/1.20/builds/578/downloads/waterfall-1.20-578.jar"

// 下载 Waterfall（写 EULA 对代理无害，复用 TabooLib 内置任务）。
val prepareE2EProxy by tasks.registering(PrepareMinecraftServerEnvTask::class) {
    group = "beacon-e2e"
    description = "下载 Waterfall 代理 jar"
    jarUrl.set(waterfallUrl)
    jarName.set(proxyJarName)
    serverDirectory.set(runDir)
    agreeEula.set(true)
}

val runBungee by tasks.registering(JavaExec::class) {
    group = "beacon-e2e"
    description = "部署 BeaconAgentProxy + E2E 代理插件到 Waterfall 并启动（端到端验收，代理侧）"

    dependsOn(prepareE2EProxy)
    dependsOn(":agent-bungee:build")
    dependsOn("build")

    doFirst {
        val pluginsDir = runDir.resolve("plugins")
        pluginsDir.mkdirs()

        // 拷贝 BeaconAgentProxy 数据面插件（agent-bungee 的最终 jar）。
        val agentJar = project(":agent-bungee").layout.buildDirectory
            .file("libs/BeaconAgentProxy-${project.version}.jar").get().asFile
        require(agentJar.exists()) { "未找到 BeaconAgentProxy jar：${agentJar.absolutePath}" }
        agentJar.copyTo(pluginsDir.resolve("BeaconAgentProxy.jar"), overwrite = true)

        // 拷贝本验收插件的最终 jar。
        val e2eJar = layout.buildDirectory.file("libs/BeaconE2EProxy-${project.version}.jar").get().asFile
        require(e2eJar.exists()) { "未找到 BeaconE2EProxy jar：${e2eJar.absolutePath}" }
        e2eJar.copyTo(pluginsDir.resolve("BeaconE2EProxy.jar"), overwrite = true)

        // 放置 agent 的 config.yml 到其数据目录（serverId 用 proxy 专属，namespace 与控制面一致）。
        val beaconEndpoint = (project.findProperty("e2eBeaconEndpoint") as String?) ?: "http://localhost:8848"
        val serverId = (project.findProperty("e2eServerId") as String?) ?: "e2e-bungee-1"
        val namespace = (project.findProperty("e2eNamespace") as String?) ?: "prod"
        val agentDataDir = pluginsDir.resolve("BeaconAgentProxy")
        agentDataDir.mkdirs()
        agentDataDir.resolve("config.yml").writeText(
            agentConfigYaml(beaconEndpoint, serverId, namespace),
            Charsets.UTF_8,
        )

        // TabooLib 运行期仓库覆盖（避开已下线的 sacredcraft.cn）。
        val tabooRepo = (project.findProperty("e2eTabooRepo") as String?)
            ?: "https://repo.tabooproject.org/repository/releases"
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

    workingDir = runDir
    classpath = files(runDir.resolve(proxyJarName))
    jvmArgs = listOf("-Xms256M", "-Xmx768M", "-Dfile.encoding=UTF-8")
    // Waterfall/BungeeCord 无 nogui 参数，直接启动；控制台读 stdin。
    standardInput = System.`in`
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
