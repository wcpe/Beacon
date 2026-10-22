package top.wcpe.beacon.agent.bungee

import net.md_5.bungee.api.ProxyServer
import taboolib.common.LifeCycle
import taboolib.common.env.RuntimeDependencies
import taboolib.common.env.RuntimeDependency
import taboolib.common.platform.Awake
import taboolib.common.platform.Plugin
import taboolib.common.platform.function.getDataFolder
import taboolib.common.platform.function.info
import taboolib.common.platform.function.pluginVersion
import taboolib.common.platform.function.severe
import taboolib.common.platform.function.submitAsync
import taboolib.common.platform.function.warning
import taboolib.module.configuration.Config
import taboolib.module.configuration.Configuration
import top.wcpe.beacon.agent.adapters.HttpRosterDirectory
import top.wcpe.beacon.agent.adapters.KotlinxJsonCodec
import top.wcpe.beacon.agent.adapters.OkHttpBlobStreamTransport
import top.wcpe.beacon.agent.adapters.OkHttpStreamTransport
import top.wcpe.beacon.agent.adapters.OkHttpTransport
import top.wcpe.beacon.agent.api.BeaconAgentProvider
import top.wcpe.beacon.agent.core.AgentAssembly
import top.wcpe.beacon.agent.core.AssembledAgent
import top.wcpe.beacon.agent.core.AssemblyHooks
import top.wcpe.beacon.agent.core.ConfigContext
import top.wcpe.beacon.agent.core.TransportConfig
import top.wcpe.beacon.agent.core.api.EffectiveConfigView
import top.wcpe.beacon.agent.core.api.mapDiscoveryResult
import top.wcpe.beacon.agent.core.client.ActiveBinding
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.DiscoveryFilters
import top.wcpe.beacon.agent.core.client.discoverResult
import top.wcpe.beacon.agent.core.client.register
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.connection.ConnectionEventBuffer
import top.wcpe.beacon.agent.core.connection.ConnectionReportCoordinator
import top.wcpe.beacon.agent.core.connection.ProxyConnectionTracker
import top.wcpe.beacon.agent.core.identity.AgentCredentialsStore
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.identity.AgentIdentityStore
import top.wcpe.beacon.agent.core.identity.EndpointReport
import top.wcpe.beacon.agent.core.identity.IdentityBindingSnapshotStore
import top.wcpe.beacon.agent.core.identity.ProxyListenerEndpoint
import top.wcpe.beacon.agent.core.identity.toCredentialsBinding
import top.wcpe.beacon.agent.core.lifecycle.AgentLifecycle
import top.wcpe.beacon.agent.core.lifecycle.BootstrapRuntime
import top.wcpe.beacon.agent.core.messaging.MessagingRuntime
import top.wcpe.beacon.agent.core.messaging.RosterDirectoryHolder
import top.wcpe.beacon.agent.core.proxy.DirectorySyncLoopGate
import top.wcpe.beacon.agent.core.proxy.InitialLobbyRouter
import top.wcpe.beacon.agent.core.proxy.ProxyServerDirectorySyncer
import top.wcpe.beacon.agent.core.settings.AgentBootstrap
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.EffectiveSettingsResolver
import top.wcpe.beacon.agent.core.settings.EnvOverridingConfigReader
import top.wcpe.beacon.agent.core.settings.PushedConfigStore
import java.io.File
import java.util.UUID
import java.util.concurrent.atomic.AtomicReference

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
)
object BeaconAgentBungee : Plugin() {
    /** agent 引导配置（资源 config.yml 随 jar 释放到数据目录）。 */
    @Config("config.yml")
    lateinit var config: Configuration

    /** JSON 编解码（无状态，凭据与下发缓存共用）。 */
    private val jsonCodec = KotlinxJsonCodec()

    /** 身份凭据存储（FR-276）：join 命令与启动流程共用同一实例。 */
    private var credentials: AgentCredentialsStore? = null

    /** 合并凭据后的生效设置。 */
    private var effectiveSettings: AgentSettings? = null

    /**
     * 生效设置解析器（三层优先级 + 凭据合并）。
     *
     * 惰性创建：`config` 在 ENABLE 阶段才注入，构造期取不到。
     * 凭据存储复用 `credentials`，**不另建实例**：join 命令持有的是 enable 里那个实例，
     * 若这里再造一个，两边的写锁互相不可见，读-改-写会交错丢字段（绑定或令牌被覆盖）。
     */
    private val settingsResolver: EffectiveSettingsResolver by lazy {
        EffectiveSettingsResolver(
            localReader = EnvOverridingConfigReader(TabooLibConfigReader(config), System::getenv),
            pushedStore = PushedConfigStore(getDataFolder().toPath(), jsonCodec),
            credentialsStore =
                credentials
                    ?: AgentCredentialsStore(
                        getDataFolder().toPath(),
                        jsonCodec,
                        warn = ::warning,
                        error = { severe(it) },
                    ).also { credentials = it },
            warn = ::warning,
            info = ::info,
        )
    }

    /** 当前生命周期；null 表示尚未接入或已停止。 */
    private var lifecycle: AgentLifecycle? = null

    /** pending 阶段唯一运行的最小身份引导器。 */
    private var bootstrapRuntime: BootstrapRuntime? = null

    /** 仅存活于本进程、且只能来自控制面 active 响应的绑定。 */
    private var confirmedBinding: ActiveBinding? = null

    /** 本轮 active runtime 创建的 BC 目录；停止时必须撤回接管状态。 */
    private var serverDirectory: BungeeServerDirectory? = null

    /** Proxy 服务器目录同步递归链单飞代际门；disable 时失效当前代际。 */
    private val directorySyncLoopGate = DirectorySyncLoopGate()

    /** BC 专属指标缓存（FR-144）；null 表示未装配。 */
    private var proxyMetricsCache: BungeeProxyMetricsCache? = null

    /** 连接明细批上报协调器（FR-145）；null 表示未装配。 */
    private var connectionReporter: ConnectionReportCoordinator? = null

    /** 跨服消息模块运行时（FR-149，HTTP 中转）；null 表示未装配。随注册自启，DISABLE 时 stop。 */
    private var messagingRuntime: MessagingRuntime? = null

    /** 玩家名册持有者（FR-31）；null 表示无活跃运行时。Disable / 撤销时 reset 复位，避免业务插件读到已停用身份的名册。 */
    private var rosterDirectoryHolder: RosterDirectoryHolder? = null

    /** 旧小区默认入口配置只兼容读取一次，不再参与 BC 首次大厅落脚。 */
    @Volatile
    private var legacyProxyWarningLogged = false

    /**
     * 可重入的装配动作（join 成功后需再执行一次）。
     *
     * 用 lambda 字段而不是私有函数：本对象已贴 detekt 的 TooManyFunctions 阈值，
     * 再加一个函数就超限；而这段逻辑只需要「能被 enable 与 join 各触发一次」。
     */
    private val doAssemble: () -> Unit = {
        bootstrapRuntime?.shutdown()
        bootstrapRuntime = null
        // 每次重入都重读配置：join 刚写过凭据，必须拿到最新的地址与令牌。
        val activeSettings = settingsResolver.resolve()
        effectiveSettings = activeSettings
        // 复用 enable 阶段建好的同一实例：join、设置解析、身份解析与绑定落盘必须读写同一个文件。
        val credentialsStore =
            credentials
                ?: AgentCredentialsStore(
                    getDataFolder().toPath(),
                    jsonCodec,
                    warn = ::warning,
                    error = { severe(it) },
                ).also { credentials = it }
        val endpointReport =
            EndpointReport(
                proxyListeners =
                    ProxyServer.getInstance().config.listeners.mapIndexed { ordinal, listener ->
                        val host = listener.host
                        ProxyListenerEndpoint(host.hostString, host.port, ordinal)
                    },
            )
        submitAsync {
            val identityStore = AgentIdentityStore(getDataFolder().toPath())
            // 只读不新建：旧实现走 loadOrCreate()，文件缺失时会落盘一个从未在控制面注册过的新 UUID。
            // 凭据才是身份真源，那个 UUID 只会在凭据变得不可读时被迁移「复活」成一台新服务器。
            val storedIdentity = identityStore.loadExisting()
            // fail-fast（保留既有语义）：旧身份文件损坏、且凭据里也没有合法身份，才是真的丢了身份。
            // 反过来，凭据里已有合法身份时旧文件损坏不挡接入——凭据才是单一真源。
            if (!storedIdentity.isValid && credentialsStore.load() == null) {
                severe("身份文件损坏：${storedIdentity.error}，Beacon agent 不接入控制面")
                return@submitAsync
            }
            // 身份单一真源（FR-276）：凭据 > 旧 identity.yml（迁移一次） > 新生成并落盘。
            val identityId = credentialsStore.resolveIdentityId(storedIdentity)
            // 把 identity.yml 对齐成权威身份：两份文件从此永远指向同一个 ID，
            // 历史上残留的休眠 UUID 就地纠正，迁移路径再也不会把它复活（取舍见 alignTo 注释）。
            identityStore.alignTo(identityId)
            // 服务器工作目录（FR-226）：agent dataFolder 的父（plugins）的父 = 代理根。
            val serverWorkDir = getDataFolder().absoluteFile.parentFile?.parentFile?.absolutePath.orEmpty()
            // 角色按壳固定为 bungee；agent 构建版本经 TabooLib pluginVersion 注入（FR-86，见 ADR-0039）。
            val identity =
                AgentBootstrap.readIdentity(role = "bungee", agentVersion = pluginVersion, serverWorkDir = serverWorkDir)
                    .copy(identityId = identityId, bootId = UUID.randomUUID().toString(), endpointReport = endpointReport)

            confirmedBinding?.takeIf { it.namespace.isNotBlank() && it.serverId.isNotBlank() }?.let {
                identity.bind(it.namespace, it.serverId)
            }

            // 未接入 ≠ 配置错误（FR-276）：未 join 时静静等待，不刷 ERROR。
            if (!settingsResolver.hasConnectionConfig(activeSettings)) {
                return@submitAsync
            }

            // 装配：先建 store + view，再用 view 构 adapter（adapter 在变更时回调 view 派发 API 监听器）。
            val store = EffectiveConfigStore()
            val view =
                EffectiveConfigView(store, activeSettings.configReloadTimeoutMs, reloadPoolSize = activeSettings.configReloadPoolSize)
            val adapter = BungeePlatformAdapter(view)
            warnLegacyProxySettings(adapter, activeSettings)
            val codec = KotlinxJsonCodec()
            val bindingSnapshot = IdentityBindingSnapshotStore(File(getDataFolder(), "identity-binding.snapshot.json"), codec)
            if (confirmedBinding == null) {
                startBootstrap(identity, activeSettings, adapter, codec, bindingSnapshot)
                return@submitAsync
            }
            // 装配一次稳定依赖组，供 active 阶段各私有方法复用，避免长参数列表。
            val deps = BungeeRuntimeDeps(activeSettings, adapter, codec, store, view, bindingSnapshot)
            startActiveRuntime(identity, deps)
        }
    }

    @Awake(LifeCycle.ENABLE)
    fun enable() {
        // 身份凭据存储（FR-276）：地址 + 令牌，与 config.yml 分文件保存。
        // 这里只建实例不读值——真正的读取在 startBootstrap() 里，因为 join 之后要重读。
        val credentialsStore =
            AgentCredentialsStore(
                getDataFolder().toPath(),
                jsonCodec,
                // 告警出口必须在这里接上：Windows 上「创建即 0600」必然走降级分支，
                // 不接线的话运维看不到任何提示（护栏「任何降级都要显式呈现」）。
                warn = ::warning,
                error = { severe(it) },
            )
        credentials = credentialsStore
        // 命令注册提到接入之前（FR-276）：/beacon join 是接入入口，等接入成功才注册就永远执行不到。
        BeaconJoinCommand.register(
            credentials = credentialsStore,
            onJoined = { doAssemble() },
            logger = BeaconJoinCommand.JoinLogger(info = ::info, warning = ::warning),
        )
        doAssemble()
    }

    /** pending 阶段：身份已就绪但尚未获控制面确认绑定时，仅启最小引导器，绝不提前创建数据面组件。 */
    private fun startBootstrap(
        identity: AgentIdentity,
        settings: AgentSettings,
        adapter: BungeePlatformAdapter,
        codec: KotlinxJsonCodec,
        bindingSnapshot: IdentityBindingSnapshotStore,
    ) {
        // 复用 enable 阶段建好的同一实例：join、设置解析、身份解析与绑定落盘必须读写同一个文件。
        val credentialsStore =
            credentials
                ?: AgentCredentialsStore(
                    getDataFolder().toPath(),
                    jsonCodec,
                    warn = ::warning,
                    error = { severe(it) },
                ).also { credentials = it }
        val bootstrap =
            BootstrapRuntime(
                identity = identity,
                settings = settings,
                adapter = adapter,
                apiClient = BeaconApiClient(OkHttpTransport(connectTimeoutMs = settings.requestTimeoutMs), codec, settings),
                snapshots = bindingSnapshot,
                onActive = { activeIdentity, binding ->
                    // 归属确认即落盘（FR-276）：join 的「已绑定则拒绝」守卫读的就是这份 binding，
                    // 不写盘则该守卫永不可达。写盘失败不影响接入——绑定是控制面权威事实，
                    // 内存里已生效，磁盘只为下次启动与 join 判断可见。
                    runCatching { credentialsStore.updateBinding(binding.toCredentialsBinding(activeIdentity.role)) }
                        .onFailure { warning("写入绑定凭据失败（不影响本次接入）：${it.javaClass.simpleName}: ${it.message}") }
                    confirmedBinding = binding
                    enable()
                },
                onTerminal = {
                    bindingSnapshot.invalidate()
                    stopActiveRuntime()
                },
            )
        bootstrapRuntime = bootstrap
        bootstrap.start()
    }

    /** active 阶段：已获控制面确认绑定，装配完整运行时并接入。 */
    private fun startActiveRuntime(
        identity: AgentIdentity,
        deps: BungeeRuntimeDeps,
    ) {
        // 单一代理目录实例：同时供目录同步（注入子服）与后端归属上报（读当前后端集合，FR-36）。
        val serverDirectory = BungeeServerDirectory()
        this.serverDirectory = serverDirectory
        // 装配期先留空，目录同步器创建后回填；lifecycle 在回填后才启动，命令不会命中空引用。
        val directorySyncerRef = AtomicReference<ProxyServerDirectorySyncer?>(null)
        // BC 专属指标缓存（FR-144）：慢刷后端可达性，使 1s 采样只读缓存不被阻塞探测拖住。
        val proxyCache = BungeeProxyMetricsCache(deps.adapter)
        proxyMetricsCache = proxyCache
        val assembled = assembleRuntime(identity, deps, serverDirectory, proxyCache, directorySyncerRef)
        lifecycle = assembled.lifecycle
        // 跨服消息模块（FR-149，HTTP 中转）：随注册成功自启（AgentAssembly 已挂 onRegistered），此处仅留引用供 DISABLE 停止。
        messagingRuntime = assembled.messaging.messagingRuntime
        // 玩家名册持有者（FR-31）：装配期已随 Discovery 门面创建，此处留引用供 DISABLE 复位。
        rosterDirectoryHolder = assembled.messaging.rosterDirectoryHolder
        assembled.lifecycle.onRegistered {
            val binding = confirmedBinding ?: return@onRegistered
            deps.bindingSnapshot.write(identity, binding)
        }
        assembled.lifecycle.onRegistered { proxyCache.start() }

        // 对外注册门面，供同进程业务插件读取。
        BeaconAgentProvider.register(assembled.beaconAgent)

        wireDirectorySync(deps.adapter, assembled, serverDirectory, directorySyncerRef, identity)
        wireRosterAndConnection(identity, deps, assembled)

        // 启用 v2 指标 1s 采样 + 5s 批上报（FR-144）：须在接入前开启，注册成功即启两条循环。
        assembled.lifecycle.enableMetricsSampling()

        // 先点亮快照再异步接入，不阻塞主线程。
        assembled.lifecycle.bootstrapWithSnapshotThenConnect()
    }

    /** 装配 core 运行时（AgentAssembly.assemble），集中 assemble 的长参数列表。 */
    private fun assembleRuntime(
        identity: AgentIdentity,
        deps: BungeeRuntimeDeps,
        serverDirectory: BungeeServerDirectory,
        proxyCache: BungeeProxyMetricsCache,
        directorySyncerRef: AtomicReference<ProxyServerDirectorySyncer?>,
    ): AssembledAgent =
        AgentAssembly.assemble(
            identity = identity,
            settings = deps.settings,
            // FR-88：传原始 adapter，assemble 内部用 BufferingPlatformAdapter 包裹以旁路采集日志环形缓冲。
            rawAdapter = deps.adapter,
            transport =
                TransportConfig(
                    transport = OkHttpTransport(connectTimeoutMs = deps.settings.requestTimeoutMs),
                    codec = deps.codec,
                    // 单条 SSE 推送流（FR-24）：取代配置/文件树/覆盖集三条长轮询，纯 HTTP 读流、无重型依赖。
                    streamTransport = OkHttpStreamTransport(connectTimeoutMs = deps.settings.requestTimeoutMs),
                    // 交付 blob 流式传输（FR-165，见 ADR-0069）：启用交付数据面（上传 / 下载 blob），流式不整读入内存。
                    blobStreamTransport = OkHttpBlobStreamTransport(connectTimeoutMs = deps.settings.requestTimeoutMs),
                ),
            config = ConfigContext(deps.store, deps.view),
            hooks =
                AssemblyHooks(
                    // 运行指标供给（FR-32）：上报时采代理在线人数 + JVM 内存 / CPU 真值（代理无 TPS，恒 0）。
                    metricsProvider = { BungeeMetricsCollector.sample() },
                    // 后端归属供给（FR-36）：注册/上报时取本代理当前代理的后端子服 serverId 集合（仅 bc 填）。
                    backendsProvider = { serverDirectory.backendServerIds().toList() },
                    // BC 专属指标供给（FR-34 / FR-144）：现采连接 / 线程 / 运行时长 + 缓存的后端可达性（仅 bc 填，不阻塞采样）。
                    proxyMetricsProvider = { proxyCache.current() },
                    // 自我保护：把本壳 plugin 名注入 applier 作受保护顶段，命中即跳过——杜绝运维误把
                    // plugins/BeaconAgentProxy/* 经 FR-14 文件树或 FR-38 导入塞进有效树后覆写自身（与 FR-41 env 注入身份呼应）。
                    selfPluginDirNames = setOf("BeaconAgentProxy"),
                    onBcDirectoryResync = { directorySyncerRef.get()?.syncOnce() ?: false },
                    authorityInvalidated = {
                        deps.bindingSnapshot.invalidate()
                        stopActiveRuntime()
                    },
                ),
        )

    /** 装配 BC 子服目录同步器、本地查询命令、首次大厅路由监听与目录同步递归链。 */
    private fun wireDirectorySync(
        adapter: BungeePlatformAdapter,
        assembled: AssembledAgent,
        serverDirectory: BungeeServerDirectory,
        directorySyncerRef: AtomicReference<ProxyServerDirectorySyncer?>,
        identity: AgentIdentity,
    ) {
        val directorySyncer =
            ProxyServerDirectorySyncer(
                directory = serverDirectory,
                warn = { adapter.warn(it) },
                info = { adapter.info(it) },
                lobbySnapshot = assembled.lobbySnapshotProvider,
            ) {
                mapDiscoveryResult(
                    assembled.apiClient.discoverResult(
                        DiscoveryFilters(
                            namespace = identity.namespace,
                            group = null,
                            zone = null,
                            role = "bukkit",
                        ),
                        identity = identity,
                    ),
                )
            }
        directorySyncerRef.set(directorySyncer)
        // BC 的本地查询命令只读取目录同步器已原子发布的内存快照。
        BeaconAgentCommand.register(assembled.lifecycle, adapter, directorySyncer::snapshot)
        BungeeInitialLobbyListener.start(
            router = InitialLobbyRouter(directorySyncer::snapshot, assembled.lobbySnapshotProvider),
            directory = serverDirectory,
        )
        assembled.lifecycle.onRegistered {
            directorySyncLoopGate.start { generation ->
                adapter.runAsync { syncDirectoryLoop(adapter, directorySyncer, generation) }
            }
        }
    }

    /** 装配连接明细批上报协调器（FR-145）。 */
    private fun wireRosterAndConnection(
        identity: AgentIdentity,
        deps: BungeeRuntimeDeps,
        assembled: AssembledAgent,
    ) {
        // 连接明细采集（FR-145，proxy 专用）：登入/换服/登出 → 会话追踪 → 有界缓冲 → 每 5s 或满 200 条批上报。
        // 采集埋点零成本、上报走 async，绝不阻塞 BC 主线程；fail-static：控制面不可用照常缓冲、玩家进出服不受影响。
        val connectionBuffer = ConnectionEventBuffer()
        val reporter =
            ConnectionReportCoordinator(
                adapter = deps.adapter,
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
        // 玩家名册（FR-31）：注册成功后才注入控制面 HTTP 名册适配器——端点按 v2 鉴权身份圈定 namespace，
        // 未注册时请求必被 401 拒绝，提前注入没有意义（代理侧业务插件与子服侧读的是同一份控制面权威名册）。
        assembled.lifecycle.onRegistered {
            assembled.messaging.rosterDirectoryHolder.set(HttpRosterDirectory(assembled.apiClient, identity))
        }
    }

    private fun syncDirectoryLoop(
        adapter: BungeePlatformAdapter,
        syncer: ProxyServerDirectorySyncer,
        generation: DirectorySyncLoopGate.Generation,
    ) {
        if (!directorySyncLoopGate.isCurrent(generation)) return
        try {
            syncer.syncOnce()
        } catch (_: Exception) {
            adapter.warn("同步 Beacon 子服目录失败")
        }
        if (!directorySyncLoopGate.isCurrent(generation)) return
        adapter.runAsyncDelayed(DIRECTORY_SYNC_INTERVAL_MS) {
            if (directorySyncLoopGate.isCurrent(generation)) {
                syncDirectoryLoop(adapter, syncer, generation)
            }
        }
    }

    @Awake(LifeCycle.DISABLE)
    fun disable() {
        bootstrapRuntime?.shutdown()
        stopActiveRuntime()
    }

    /** 控制面撤销与插件卸载共用的 active 资源停止入口。 */
    private fun stopActiveRuntime() {
        directorySyncLoopGate.stop()
        BungeeInitialLobbyListener.stop()
        serverDirectory?.resetManaged()
        serverDirectory = null
        BungeeConnectionListener.tracker = null
        connectionReporter?.stop()
        connectionReporter = null
        messagingRuntime?.stop()
        messagingRuntime = null
        // 名册复位为未注入：停用后本机身份已失效（读取只会 401），持旧引用无意义，复位即让业务插件走降级。
        rosterDirectoryHolder?.reset()
        rosterDirectoryHolder = null
        proxyMetricsCache?.stop()
        proxyMetricsCache = null
        lifecycle?.shutdown()
        lifecycle = null
        bootstrapRuntime?.shutdown()
        bootstrapRuntime = null
        confirmedBinding = null
        BeaconAgentProvider.unregister()
    }

    private fun warnLegacyProxySettings(
        adapter: BungeePlatformAdapter,
        settings: AgentSettings,
    ) {
        if (legacyProxyWarningLogged || (settings.proxy.homeGroup.isBlank() && settings.proxy.homeZone.isBlank())) return
        legacyProxyWarningLogged = true
        adapter.warn("proxy.home-group/home-zone 已废弃并被忽略；首次入服由控制面大厅集群决定")
    }

    private const val DIRECTORY_SYNC_INTERVAL_MS = 10_000L
}

/**
 * active 阶段各私有方法复用的稳定依赖组（enable 阶段装配一次，随每次 active 接入复用）。
 * 仅封装在 enable 内一次性创建、且被 startActiveRuntime / assembleRuntime / wireRosterAndConnection 共享的对象，
 * 各方法特有的临时对象（identity / assembled / serverDirectory / proxyCache / directorySyncerRef）仍作为独立参数传递。
 */
private data class BungeeRuntimeDeps(
    val settings: AgentSettings,
    val adapter: BungeePlatformAdapter,
    val codec: KotlinxJsonCodec,
    val store: EffectiveConfigStore,
    val view: EffectiveConfigView,
    val bindingSnapshot: IdentityBindingSnapshotStore,
)
