// agent-e2e：M6 端到端验收用的 TabooLib Bukkit 插件，产出 BeaconE2E.jar。
// 作为「业务插件」compileOnly 依赖 agent-api，经只读 API 读取约定 dataId 并把观测写标记文件。
// 另注册 runServer 任务：用 TabooLib 的 PrepareMinecraftServerEnvTask 自动下载 Paper、写 EULA，
// 再把 BeaconAgent.jar 与本插件 jar 放进 plugins 目录后启动服务端，供整条 E2E 编排驱动。
import io.izzel.taboolib.gradle.PrepareMinecraftServerEnvTask

plugins {
    kotlin("jvm")
    id("io.izzel.taboolib")
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
// runServer：自动下载 Paper + 部署插件 + 启动服务端（M6 端到端编排入口）。
// ---------------------------------------------------------------------------

// 运行根目录（落 .tmp，不入库）。
val runDir = rootProject.projectDir.resolve("../.tmp/e2e-run/bukkit")
// 服务端 jar 文件名。
val serverJarName = "paper.jar"
// Paper 1.20.4 指定构建的直链（Java 21 可运行）。可经 -Pe2ePaperUrl 覆盖。
val paperUrl = (project.findProperty("e2ePaperUrl") as String?)
    ?: "https://api.papermc.io/v2/projects/paper/versions/1.20.4/builds/499/downloads/paper-1.20.4-499.jar"

// 下载 Paper 并写 EULA：复用 TabooLib 内置任务，自动处理 eula.txt。
val prepareE2EServer by tasks.registering(PrepareMinecraftServerEnvTask::class) {
    group = "beacon-e2e"
    description = "下载 Paper 服务端并写入 EULA"
    jarUrl.set(paperUrl)
    jarName.set(serverJarName)
    serverDirectory.set(runDir)
    agreeEula.set(true)
}

// 把 BeaconAgent.jar、本插件 jar 拷进 plugins，并放置 agent 的 config.yml，再启动服务端。
val runServer by tasks.registering(JavaExec::class) {
    group = "beacon-e2e"
    description = "部署 BeaconAgent + E2E 插件到 Paper 并启动（端到端验收）"

    // 依赖：先备好服务端环境；再构建 agent 与本插件的最终（已 relocate）jar。
    dependsOn(prepareE2EServer)
    dependsOn(":agent-bukkit:build")
    dependsOn("build")

    // MC 监听端口：默认 25566，避让本机可能被其它 MC 服占用的 25565；经 -Pe2eMcPort 覆盖。
    val mcPort = (project.findProperty("e2eMcPort") as String?) ?: "25566"
    // agent 本地受限命令白名单（逗号分隔首 token，注入 config.yml override.command-whitelist）；
    // 默认空 = 命令派发能力关闭（ADR-0011 默认 inert）；经 -Pe2eCommandWhitelist 覆盖，如 "beacone2ereload"。
    val commandWhitelist = (project.findProperty("e2eCommandWhitelist") as String?) ?: ""

    doFirst {
        val pluginsDir = runDir.resolve("plugins")
        pluginsDir.mkdirs()

        // 拷贝 BeaconAgent 数据面插件（agent-bukkit 的最终 jar）。
        val agentJar = project(":agent-bukkit").layout.buildDirectory
            .file("libs/BeaconAgent-${project.version}.jar").get().asFile
        require(agentJar.exists()) { "未找到 BeaconAgent jar：${agentJar.absolutePath}" }
        agentJar.copyTo(pluginsDir.resolve("BeaconAgent.jar"), overwrite = true)

        // 拷贝本验收插件的最终 jar。
        val e2eJar = layout.buildDirectory.file("libs/BeaconE2E-${project.version}.jar").get().asFile
        require(e2eJar.exists()) { "未找到 BeaconE2E jar：${e2eJar.absolutePath}" }
        e2eJar.copyTo(pluginsDir.resolve("BeaconE2E.jar"), overwrite = true)

        // 放置 agent 的 config.yml（serverId / namespace / beacon.endpoints 等）到其数据目录。
        // 控制面地址默认指向 http://localhost:8848，可经 -Pe2eBeaconEndpoint 覆盖。
        val beaconEndpoint = (project.findProperty("e2eBeaconEndpoint") as String?) ?: "http://localhost:8848"
        val serverId = (project.findProperty("e2eServerId") as String?) ?: "e2e-bukkit-1"
        val namespace = (project.findProperty("e2eNamespace") as String?) ?: "prod"
        val agentDataDir = pluginsDir.resolve("BeaconAgent")
        agentDataDir.mkdirs()
        agentDataDir.resolve("config.yml").writeText(
            agentConfigYaml(beaconEndpoint, serverId, namespace, mcPort, commandWhitelist),
            Charsets.UTF_8,
        )

        // 写入 TabooLib 运行期仓库覆盖文件 env.properties（置于服务端工作目录，覆盖 jar 内 env）。
        // TabooLib 6.2.3 的 repo-reflex 默认指向已下线的 sacredcraft.cn:8081，会导致首次加载下载
        // reflex/analyser 超时而插件无法启用；这里统一改指可达的 repo.tabooproject.org。
        // 该文件被两个插件（BeaconAgent / BeaconE2E）共用，一处生效。
        val tabooRepo = (project.findProperty("e2eTabooRepo") as String?)
            ?: "https://repo.tabooproject.org/repository/releases"
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
        logger.lifecycle("控制面地址：$beaconEndpoint，serverId=$serverId，namespace=$namespace")
    }

    // 在服务端目录内以下载好的 Paper jar 启动；-nogui 无界面，控制台读 stdin。
    workingDir = runDir
    classpath = files(runDir.resolve(serverJarName))
    // 内存上限适中，避免占满宿主；jvm 参数固定，结果可复现。
    // 运行期库仓库覆盖改由工作目录的 env.properties 承担（见 doFirst），此处不再传 -D 覆盖。
    // 传 -Pe2eDebug 可打开 TabooLib 调试输出，排查插件生命周期问题。
    val baseJvm = mutableListOf("-Xms512M", "-Xmx1536M", "-Dfile.encoding=UTF-8")
    if (project.hasProperty("e2eDebug")) {
        baseJvm += "-Dtaboolib.debug=true"
    }
    jvmArgs = baseJvm
    // -nogui 无界面；--port 指定监听端口（默认 25566，避让 25565）。
    args = listOf("--nogui", "--port", mcPort)
    // 透传控制台 IO，便于在前台看日志并向服务端发 stop。
    standardInput = System.`in`
}

/** 生成 E2E 用 agent config.yml；无 zone、无 canary，仅最小接入信息 + FR-15 覆盖命令白名单。 */
fun agentConfigYaml(endpoint: String, serverId: String, namespace: String, mcPort: String, commandWhitelist: String): String {
    // 逗号分隔的白名单首 token → YAML 列表；空则渲染为内联空列表 []（命令派发能力关闭）。
    val items = commandWhitelist.split(",").map { it.trim() }.filter { it.isNotEmpty() }
    val whitelistYaml = if (items.isEmpty()) " []" else items.joinToString("") { "\n        - \"$it\"" }
    return """
    # Beacon agent E2E 运行配置（由 runServer 任务生成，仅供端到端验收）。
    beacon:
      # 控制面地址：指向本地起的 Beacon（默认 8848，与产品默认端口一致）。
      endpoints:
        - "$endpoint"
      # 共享令牌，置于请求头 X-Beacon-Token，需与控制面 agent-token 一致。
      bootstrap-token: "beacon-bootstrap-2026"

    identity:
      # 环境：必须与控制面 namespace 一致。
      namespace: "$namespace"
      # 本机唯一身份，环境内唯一。
      server-id: "$serverId"
      # 大区提示：尚未指派 zone 时作兜底 group。
      group-hint: "area1"
      # 对外可达地址 ip:port（与 --port 一致，仅作注册元数据）。
      address: "127.0.0.1:$mcPort"
      # 业务版本标签。
      version: "1.0.0"
      # 容量（发现过滤维度）。
      capacity: 200
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

    file-tree:
      # 启用文件树/覆盖通道（通道B）：FR-15 覆盖集长轮询循环依赖它开启（默认即 true，这里显式声明）。
      enabled: true

    override:
      # 受限重载命令首 token 本地白名单（逗号分隔注入；默认空=命令派发能力关闭，见 ADR-0011 决策 3）。
      command-whitelist:$whitelistYaml
""".trimIndent() + "\n"
}
