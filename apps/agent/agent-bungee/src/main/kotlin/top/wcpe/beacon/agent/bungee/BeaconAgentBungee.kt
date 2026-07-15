package top.wcpe.beacon.agent.bungee

import taboolib.common.LifeCycle
import taboolib.common.env.RuntimeDependencies
import taboolib.common.env.RuntimeDependency
import taboolib.common.platform.Awake
import taboolib.common.platform.Plugin
import taboolib.common.platform.function.getDataFolder
import taboolib.common.platform.function.pluginVersion
import taboolib.common.platform.function.severe
import taboolib.common.platform.function.submitAsync
import taboolib.module.configuration.Config
import taboolib.module.configuration.Configuration
import top.wcpe.beacon.agent.adapters.KotlinxJsonCodec
import top.wcpe.beacon.agent.adapters.OkHttpBlobStreamTransport
import top.wcpe.beacon.agent.adapters.OkHttpStreamTransport
import top.wcpe.beacon.agent.adapters.OkHttpTransport
import top.wcpe.beacon.agent.api.BeaconAgentProvider
import top.wcpe.beacon.agent.api.DiscoveryQuery
import top.wcpe.beacon.agent.core.AgentAssembly
import top.wcpe.beacon.agent.core.api.EffectiveConfigView
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.connection.ConnectionEventBuffer
import top.wcpe.beacon.agent.core.connection.ConnectionReportCoordinator
import top.wcpe.beacon.agent.core.connection.ProxyConnectionTracker
import top.wcpe.beacon.agent.core.identity.AgentIdentityStore
import top.wcpe.beacon.agent.core.lifecycle.AgentLifecycle
import top.wcpe.beacon.agent.core.messaging.MessagingRuntime
import top.wcpe.beacon.agent.core.proxy.ProxyServerDirectorySyncer
import top.wcpe.beacon.agent.core.settings.AgentBootstrap
import top.wcpe.beacon.agent.core.settings.EnvOverridingConfigReader
import java.util.UUID
import java.util.concurrent.atomic.AtomicBoolean

/**
 * BungeeCord 代理侧 Beacon agent 插件主类（object + @Awake，不继承 Plugin 基类外的内容）。
 *
 * ENABLE：读 config.yml → 构 AgentSettings + AgentIdentity（身份缺失 fail-fast）→
 *         装配 OkHttpTransport + KotlinxJsonCodec + Bungee 适配器 → bootstrap 接入。
 * DISABLE：停循环 + 注销门面。
 *
 * 第三方依赖（okhttp/okio/kotlinx，均 Kotlin 库）经 TabooLib @RuntimeDependencies 运行期下载，不打包进 jar
 * （参考 CoreLib）。transitive=false 手动列全传递依赖。relocate 与构建期 relocate 目标一致：
 * okhttp3/okio/kotlinx.serialization → top.wcpe.beacon.agent.lib.*（与 agent 自身引用对齐、且互相可见）；
 * 内部的 kotlin → kotlin1922（TabooLib 把 kotlin 1.9.22 stdlib 重定位为 kotlin1922）。test 用重定位后的类名。
 */
@RuntimeDependencies(
    RuntimeDependency(
        "!com.squareup.okhttp3:okhttp:4.12.0",
        test = "!top.wcpe.beacon.agent.lib.okhttp3.OkHttpClient",
        relocate = ["!okhttp3", "!top.wcpe.beacon.agent.lib.okhttp3", "!okio", "!top.wcpe.beacon.agent.lib.okio", "!kotlin", "!kotlin1922"],
        transitive = false,
    ),
    // okio/kotlinx 是 Kotlin 多平台库，运行期需下载 JVM 变体（-jvm），其内含实际 JVM 类（如 okio.Buffer）。
    RuntimeDependency(
        "!com.squareup.okio:okio-jvm:3.6.0",
        test = "!top.wcpe.beacon.agent.lib.okio.Buffer",
        relocate = ["!okio", "!top.wcpe.beacon.agent.lib.okio", "!kotlin", "!kotlin1922"],
        transitive = false,
    ),
    RuntimeDependency(
        "!org.jetbrains.kotlinx:kotlinx-serialization-json-jvm:1.6.3",
        test = "!top.wcpe.beacon.agent.lib.kotlinx.serialization.json.Json",
        relocate = ["!kotlinx.serialization", "!top.wcpe.beacon.agent.lib.kotlinx.serialization", "!kotlin", "!kotlin1922"],
        transitive = false,
    ),
    RuntimeDependency(
        "!org.jetbrains.kotlinx:kotlinx-serialization-core-jvm:1.6.3",
        test = "!top.wcpe.beacon.agent.lib.kotlinx.serialization.KSerializer",
        relocate = ["!kotlinx.serialization", "!top.wcpe.beacon.agent.lib.kotlinx.serialization", "!kotlin", "!kotlin1922"],
        transitive = false,
    ),
    // Redis 客户端（FR-26）：proxy 侧维护玩家位置名册 + 可参与消息。运行期下载、relocate、不打包、不经 CoreLib。
    // 关键：TabooLib 的 relocate 按依赖各自的 jar 生效，故 jedis 这条必须把它内部引用、且被本工程同样 relocate 的
    // 传递依赖（commons-pool2 / gson）一并声明 relocate，否则下载并重定位后的 jedis 仍引用原始包名
    // org.apache.commons.pool2.* / com.google.gson.*，而类路径只有重定位副本（lib.*）→ 运行期 NoClassDefFoundError。
    // slf4j 不在此列：由平台提供，保持原始包名解析，不重定位。
    RuntimeDependency(
        "!redis.clients:jedis:4.2.3",
        test = "!top.wcpe.beacon.agent.lib.redis.clients.jedis.Jedis",
        relocate = [
            "!redis.clients.jedis", "!top.wcpe.beacon.agent.lib.redis.clients.jedis",
            "!org.apache.commons.pool2", "!top.wcpe.beacon.agent.lib.org.apache.commons.pool2",
            "!com.google.gson", "!top.wcpe.beacon.agent.lib.com.google.gson",
        ],
        transitive = false,
    ),
    RuntimeDependency(
        "!org.apache.commons:commons-pool2:2.11.1",
        test = "!top.wcpe.beacon.agent.lib.org.apache.commons.pool2.ObjectPool",
        relocate = ["!org.apache.commons.pool2", "!top.wcpe.beacon.agent.lib.org.apache.commons.pool2"],
        transitive = false,
    ),
    RuntimeDependency(
        "!com.google.code.gson:gson:2.10.1",
        test = "!top.wcpe.beacon.agent.lib.com.google.gson.Gson",
        relocate = ["!com.google.gson", "!top.wcpe.beacon.agent.lib.com.google.gson"],
        transitive = false,
    ),
)
object BeaconAgentBungee : Plugin() {
    /** agent 引导配置（资源 config.yml 随 jar 释放到数据目录）。 */
    @Config("config.yml")
    lateinit var config: Configuration

    /** 当前生命周期；null 表示因身份缺失未启动。 */
    private var lifecycle: AgentLifecycle? = null

    /** Proxy 服务器目录同步循环开关；disable 时关闭，避免卸载后继续调度。 */
    private val directorySyncRunning = AtomicBoolean(false)

    /** BC 专属指标缓存（FR-144）；null 表示未装配。 */
    private var proxyMetricsCache: BungeeProxyMetricsCache? = null

    /** 玩家位置名册引导（FR-26）；null 表示未装配。 */
    private var rosterBootstrap: BungeePlayerRosterBootstrap? = null

    /** 跨服消息模块引导（FR-26）；null 表示未装配。 */
    private var messagingBootstrap: BungeeMessagingBootstrap? = null

    /** 连接明细批上报协调器（FR-145）；null 表示未装配。 */
    private var connectionReporter: ConnectionReportCoordinator? = null

    /** 跨服消息模块运行时（FR-149，HTTP 中转）；null 表示未装配。随注册自启，DISABLE 时 stop。 */
    private var messagingRuntime: MessagingRuntime? = null

    @Awake(LifeCycle.ENABLE)
    fun enable() {
        // 包一层环境变量覆盖（FR-33）：BEACON_AGENT_<点分路径大写、点/连字符转下划线> 优先于 config.yml。
        val reader = EnvOverridingConfigReader(TabooLibConfigReader(config), System::getenv)
        val settings = AgentBootstrap.readSettings(reader)
        submitAsync {
            val storedIdentity = AgentIdentityStore(getDataFolder().toPath()).loadOrCreate()
            // 角色按壳固定为 bungee；agent 构建版本经 TabooLib pluginVersion 注入（FR-86，见 ADR-0039）。
            val identity =
                AgentBootstrap.readIdentity(reader, role = "bungee", agentVersion = pluginVersion)
                    .copy(identityId = storedIdentity.identityId, bootId = UUID.randomUUID().toString())

            // fail-fast：身份缺失则打 ERROR 且不启循环（不阻断代理，仅 agent 不接入）。
            var canConnect = true
            if (!storedIdentity.isValid) {
                severe("身份文件损坏：${storedIdentity.error}，Beacon agent 不接入控制面")
                canConnect = false
            }
            if (canConnect && !identity.isValid()) {
                severe("身份缺失：identity.server-id 与 identity.namespace 必须显式配置，Beacon agent 不接入控制面")
                canConnect = false
            }
            if (canConnect && (settings.endpoints.isEmpty() || settings.bootstrapToken.isBlank())) {
                severe("配置缺失：beacon.endpoints 与 beacon.bootstrap-token 必填，Beacon agent 不接入控制面")
                canConnect = false
            }
            if (!canConnect) {
                return@submitAsync
            }

            // 装配：先建 store + view，再用 view 构 adapter（adapter 在变更时回调 view 派发 API 监听器）。
            val store = EffectiveConfigStore()
            val view = EffectiveConfigView(store)
            val adapter = BungeePlatformAdapter(view)
            // 单一代理目录实例：同时供目录同步（注入子服）与后端归属上报（读当前后端集合，FR-36）。
            val serverDirectory = BungeeServerDirectory()
            // BC 专属指标缓存（FR-144）：慢刷后端可达性，使 1s 采样只读缓存不被阻塞探测拖住。
            val proxyCache = BungeeProxyMetricsCache(adapter)
            proxyCache.start()
            proxyMetricsCache = proxyCache
            val assembled =
                AgentAssembly.assemble(
                    identity = identity,
                    settings = settings,
                    // FR-88：传原始 adapter，assemble 内部用 BufferingPlatformAdapter 包裹以旁路采集日志环形缓冲。
                    rawAdapter = adapter,
                    transport = OkHttpTransport(connectTimeoutMs = settings.requestTimeoutMs),
                    codec = KotlinxJsonCodec(),
                    store = store,
                    effectiveConfigView = view,
                    // 单条 SSE 推送流（FR-24）：取代配置/文件树/覆盖集三条长轮询，纯 HTTP 读流、无重型依赖。
                    streamTransport = OkHttpStreamTransport(connectTimeoutMs = settings.requestTimeoutMs),
                    // 交付 blob 流式传输（FR-165，见 ADR-0069）：启用交付数据面（上传 / 下载 blob），流式不整读入内存。
                    blobStreamTransport = OkHttpBlobStreamTransport(connectTimeoutMs = settings.requestTimeoutMs),
                    // 运行指标供给（FR-32）：上报时采代理在线人数 + JVM 内存 / CPU 真值（代理无 TPS，恒 0）。
                    metricsProvider = { BungeeMetricsCollector.sample() },
                    // 后端归属供给（FR-36）：注册/上报时取本代理当前代理的后端子服 serverId 集合（仅 bc 填）。
                    backendsProvider = { serverDirectory.backendServerIds().toList() },
                    // BC 专属指标供给（FR-34 / FR-144）：现采连接 / 线程 / 运行时长 + 缓存的后端可达性（仅 bc 填，不阻塞采样）。
                    proxyMetricsProvider = { proxyCache.current() },
                    // 自我保护：把本壳 plugin 名注入 applier 作受保护顶段，命中即跳过——杜绝运维误把
                    // plugins/BeaconAgentProxy/* 经 FR-14 文件树或 FR-38 导入塞进有效树后覆写自身（与 FR-41 env 注入身份呼应）。
                    selfPluginDirNames = setOf("BeaconAgentProxy"),
                )
            lifecycle = assembled.lifecycle
            // 跨服消息模块（FR-149，HTTP 中转）：随注册成功自启（AgentAssembly 已挂 onRegistered），此处仅留引用供 DISABLE 停止。
            messagingRuntime = assembled.messagingRuntime

            // 对外注册门面，供同进程业务插件读取。
            BeaconAgentProvider.register(assembled.beaconAgent)

            // 注册本地运维命令 /beacon（status/reload/reconnect/resync）。
            BeaconAgentCommand.register(assembled.lifecycle, adapter)

            val directorySyncer =
                ProxyServerDirectorySyncer(
                    directory = serverDirectory,
                    // BC 服务的 home-zone（FR-48）：据此选小区默认入口；未配 / 无命中则不设默认服并告警，不静默落任意服。
                    homeGroup = settings.proxy.homeGroup,
                    homeZone = settings.proxy.homeZone,
                    warn = { adapter.warn(it) },
                    info = { adapter.info(it) },
                ) {
                    assembled.beaconAgent.discovery().query(
                        DiscoveryQuery.builder()
                            .namespace(identity.namespace)
                            .role("bukkit")
                            .build(),
                    )
                }
            directorySyncRunning.set(true)
            assembled.lifecycle.onRegistered {
                adapter.runAsync { syncDirectoryLoop(adapter, directorySyncer) }
            }

            // 玩家位置名册引导（FR-26）：据下发 Redis 配置维护「玩家→所在子服」，供子服按玩家寻址解析。
            val roster =
                BungeePlayerRosterBootstrap(
                    settings = settings,
                    store = store,
                    codec = KotlinxJsonCodec(),
                    // 名册只读端口持有者（FR-31）：名册就绪后注入全表读，点亮 proxy 侧 Discovery.roster()/rosterInZone()。
                    rosterHolder = assembled.rosterDirectoryHolder,
                    adapter = adapter,
                )
            rosterBootstrap = roster
            BungeeRosterListener.bootstrap = roster
            // 配置变更后据下发 Redis 配置重建名册引导。
            view.onChange { _, _ -> roster.sync() }

            // 跨服消息模块引导（FR-26）：据下发 Redis 配置启动代理的消息收发（消费收件流 + on 分发 + publish/subscribe），
            // 使代理成为消息对等参与方（跨服编排控制层需接收业务消息并发布广播等）。与名册引导各持独立连接、互不影响。
            val messaging =
                BungeeMessagingBootstrap(
                    identity = identity,
                    settings = settings,
                    store = store,
                    codec = KotlinxJsonCodec(),
                    holder = assembled.messagingHolder,
                    adapter = adapter,
                )
            messagingBootstrap = messaging
            // 配置变更后重算消息模块状态（Redis 连接随有效配置下发，决策 15）。
            view.onChange { _, _ -> messaging.sync() }

            // 连接明细采集（FR-145，proxy 专用）：登入/换服/登出 → 会话追踪 → 有界缓冲 → 每 5s 或满 200 条批上报。
            // 采集埋点零成本、上报走 async，绝不阻塞 BC 主线程；fail-static：控制面不可用照常缓冲、玩家进出服不受影响。
            val connectionBuffer = ConnectionEventBuffer()
            val reporter =
                ConnectionReportCoordinator(
                    adapter = adapter,
                    apiClient = assembled.apiClient,
                    identity = identity,
                    buffer = connectionBuffer,
                    bootId = identity.bootId,
                )
            connectionReporter = reporter
            // 缓冲满阈值即触发即时上报（「满 200 条即上报」，单飞去重）。
            BungeeConnectionListener.tracker =
                ProxyConnectionTracker(sink = { event -> if (connectionBuffer.add(event)) reporter.flushNow() })
            // 随注册成功启动上报循环（幂等；未注册前采集照常入缓冲，注册后补报）。
            assembled.lifecycle.onRegistered { reporter.start() }

            // 启用 v2 指标 1s 采样 + 5s 批上报（FR-144）：须在接入前开启，注册成功即启两条循环。
            assembled.lifecycle.enableMetricsSampling()

            // 先点亮快照再异步接入，不阻塞主线程。
            assembled.lifecycle.bootstrapWithSnapshotThenConnect()
            // 快照可能已含 Redis 配置：立即尝试一次（缺失则空闲，待配置下发再起）。
            roster.sync()
            messaging.sync()
        }
    }

    private fun syncDirectoryLoop(
        adapter: BungeePlatformAdapter,
        syncer: ProxyServerDirectorySyncer,
    ) {
        if (!directorySyncRunning.get()) return
        try {
            syncer.syncOnce()
        } catch (e: Exception) {
            adapter.warn("同步 Beacon 子服目录失败：${e.message}")
        }
        adapter.runAsyncDelayed(DIRECTORY_SYNC_INTERVAL_MS) {
            syncDirectoryLoop(adapter, syncer)
        }
    }

    @Awake(LifeCycle.DISABLE)
    fun disable() {
        directorySyncRunning.set(false)
        BungeeRosterListener.bootstrap = null
        BungeeConnectionListener.tracker = null
        connectionReporter?.stop()
        messagingRuntime?.stop()
        messagingBootstrap?.stop()
        rosterBootstrap?.stop()
        proxyMetricsCache?.stop()
        lifecycle?.shutdown()
        BeaconAgentProvider.unregister()
    }

    private const val DIRECTORY_SYNC_INTERVAL_MS = 10_000L
}
