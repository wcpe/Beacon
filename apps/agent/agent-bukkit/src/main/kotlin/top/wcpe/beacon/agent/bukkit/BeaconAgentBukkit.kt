package top.wcpe.beacon.agent.bukkit

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
import top.wcpe.beacon.agent.core.AssemblyHooks
import top.wcpe.beacon.agent.core.ConfigContext
import top.wcpe.beacon.agent.core.TransportConfig
import top.wcpe.beacon.agent.core.api.EffectiveConfigView
import top.wcpe.beacon.agent.core.client.ActiveBinding
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.register
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.identity.AgentCredentialsStore
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.identity.AgentIdentityStore
import top.wcpe.beacon.agent.core.identity.EndpointReport
import top.wcpe.beacon.agent.core.identity.IdentityBindingSnapshotStore
import top.wcpe.beacon.agent.core.identity.toCredentialsBinding
import top.wcpe.beacon.agent.core.lifecycle.AgentLifecycle
import top.wcpe.beacon.agent.core.lifecycle.BootstrapRuntime
import top.wcpe.beacon.agent.core.messaging.MessagingRuntime
import top.wcpe.beacon.agent.core.messaging.RosterDirectoryHolder
import top.wcpe.beacon.agent.core.settings.AgentBootstrap
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.EffectiveSettingsResolver
import top.wcpe.beacon.agent.core.settings.EnvOverridingConfigReader
import top.wcpe.beacon.agent.core.settings.PushedConfigStore
import java.io.File
import java.util.UUID

/**
 * Bukkit 子服侧 Beacon agent 插件主类（object + @Awake，不继承 JavaPlugin）。
 *
 * ENABLE：读 config.yml → 构 AgentSettings + AgentIdentity（身份缺失 fail-fast）→
 *         装配 OkHttpTransport + KotlinxJsonCodec + Bukkit 适配器 → bootstrap 接入。
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
object BeaconAgentBukkit : Plugin() {
    /** agent 引导配置（资源 config.yml 随 jar 释放到数据目录）。 */
    @Config("config.yml")
    lateinit var config: Configuration

    /** JSON 编解码（无状态，凭据与下发缓存共用）。 */
    private val jsonCodec = KotlinxJsonCodec()

    /** 身份凭据存储（FR-276）：join 命令与启动流程共用同一个实例。 */
    private var credentials: AgentCredentialsStore? = null

    /** 合并了凭据后的生效设置（endpoints/token 可能来自凭据文件）。 */
    private var effectiveSettings: AgentSettings? = null

    private var lifecycle: AgentLifecycle? = null

    /** 待确认阶段仅运行此最小引导器，绝不提前创建数据面组件。 */
    private var bootstrapRuntime: BootstrapRuntime? = null

    /** 主线程指标埋点（FR-144）；null 表示未启动（身份缺失等）。 */
    private var tickInstrumentation: BukkitTickInstrumentation? = null

    /** 跨服消息模块运行时（FR-149，HTTP 中转）；null 表示未装配。随注册自启，DISABLE 时 stop。 */
    private var messagingRuntime: MessagingRuntime? = null

    /** 玩家名册持有者（FR-31）；null 表示无活跃运行时。Disable / 撤销时 reset 复位，避免业务插件读到已停用身份的名册。 */
    private var rosterDirectoryHolder: RosterDirectoryHolder? = null

    /** startActiveRuntime 的稳定依赖组（enable 阶段装配一次，随每次 active 接入复用）。 */
    private data class ActiveRuntimeDeps(
        val settings: top.wcpe.beacon.agent.core.settings.AgentSettings,
        val adapter: BukkitPlatformAdapter,
        val codec: KotlinxJsonCodec,
        val store: EffectiveConfigStore,
        val view: EffectiveConfigView,
        val instrumentation: BukkitTickInstrumentation,
        val snapshots: IdentityBindingSnapshotStore,
    )

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
        // 命令注册**提到接入之前**（FR-276）：/beacon join 是接入的入口，
        // 若等接入成功后才注册，未接入的机器永远执行不到它——那是最需要它的时候。
        // 此时 lifecycle 还不存在，故先注册「仅接入相关」的命令集，接入成功后再补运维子命令。
        BeaconJoinCommand.register(
            credentials = credentialsStore,
            onJoined = { startBootstrap() },
            logger = BeaconJoinCommand.JoinLogger(info = ::info, warning = ::warning),
        )
        startBootstrap()
    }

    /** 统一的生效设置解析器（三层优先级 + 凭据合并，规则见 EffectiveSettingsResolver）。 */
    private fun settingsResolver(): EffectiveSettingsResolver =
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

    /**
     * 组装并启动接入（**可重入**：join 成功后再次触发）。
     *
     * 这段逻辑原先内联在 `enable()` 里，导致「未接入的机器无法再次尝试」——
     * 而 join 恰恰要求「保存凭据后立刻连上」，故必须抽出来。
     * 重入前先停掉上一轮 bootstrap，避免两个运行时并存。
     */
    private fun startBootstrap() {
        bootstrapRuntime?.shutdown()
        bootstrapRuntime = null
        // 每次重入都重读配置：join 刚写过凭据，必须拿到最新的地址与令牌。
        val activeSettings = settingsResolver().resolve()
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
        val endpointReport = EndpointReport(backendListenPort = readListenPort())
        // 容量上限（FR-228）：主线程反射读 Paper `max-players`，取回 0（→ 控制面 capacity 因子判「不适用」）。
        val maxPlayers = readMaxPlayers()
        submitAsync {
            // 身份单一真源（FR-276）：凭据 > 旧 identity.yml（迁移一次） > 新生成并落盘。
            val identity = resolveIdentity(credentialsStore, endpointReport, maxPlayers) ?: return@submitAsync

            // 合并凭据后的生效设置：地址/令牌可能来自 agent-credentials.json。
            val active = activeSettings
            // **未接入 ≠ 配置错误**（FR-276）。
            //
            // 零配置的目标形态是「装上什么都不干」：运维还没 join 时，agent 静静地等着就好，
            // 不该刷 ERROR 让人以为装坏了。故这里降为 INFO，并且**不进入重连循环**
            // （否则会无限重试打日志，把真正的故障淹掉）。
            //
            // 只有「配了但配错」（例如配了 endpoints 却没令牌）才值得 warning。
            if (!settingsResolver().hasConnectionConfig(active)) {
                return@submitAsync
            }

            // 主线程指标埋点（FR-144）：MC 主线程每 tick 零成本埋点（tick 计数 / 在线 volatile），
            // 采样 / 上报线程只读 volatile 推算，绝不在别的线程调线程不安全的 Bukkit API。
            val instrumentation = BukkitTickInstrumentation()
            tickInstrumentation = instrumentation

            // 装配：先建 store + view，再用 view 构 adapter（adapter 在变更时回调 view 派发 API 监听器）。
            val store = EffectiveConfigStore()
            val view =
                EffectiveConfigView(store, activeSettings.configReloadTimeoutMs, reloadPoolSize = activeSettings.configReloadPoolSize)
            val adapter = BukkitPlatformAdapter(view)
            val codec = KotlinxJsonCodec()
            val bindingSnapshot = IdentityBindingSnapshotStore(File(getDataFolder(), "identity-binding.snapshot.json"), codec)
            val bootstrapClient =
                BeaconApiClient(OkHttpTransport(connectTimeoutMs = activeSettings.requestTimeoutMs), codec, activeSettings)
            val bootstrap =
                BootstrapRuntime(
                    identity = identity,
                    settings = activeSettings,
                    adapter = adapter,
                    apiClient = bootstrapClient,
                    snapshots = bindingSnapshot,
                    onActive = { activeIdentity, binding ->
                        // 归属确认即落盘（FR-276）：join 的「已绑定则拒绝」守卫读的就是这份 binding，
                        // 不写盘则该守卫永不可达。写盘失败不影响接入——绑定是控制面权威事实，
                        // 内存里已生效，磁盘只为下次启动与 join 判断可见。
                        runCatching { credentialsStore.updateBinding(binding.toCredentialsBinding(activeIdentity.role)) }
                            .onFailure { warning("写入绑定凭据失败（不影响本次接入）：${it.javaClass.simpleName}: ${it.message}") }
                        startActiveRuntime(
                            identity = activeIdentity,
                            binding = binding,
                            deps = ActiveRuntimeDeps(activeSettings, adapter, codec, store, view, instrumentation, bindingSnapshot),
                        )
                    },
                    onTerminal = {
                        bindingSnapshot.invalidate()
                        stopActiveRuntime()
                    },
                )
            bootstrapRuntime = bootstrap
            bootstrap.start()
        }
    }

    /**
     * 解析本轮接入要用的身份（单一真源入口）。
     *
     * fail-fast 保留既有语义：旧身份文件损坏、且凭据里也没有合法身份，才是真的丢了身份。
     * 反过来，凭据里已有合法身份时旧文件损坏不挡接入——凭据才是单一真源。
     *
     * @param capacity 容量上限（FR-228），取不到传 0
     * @return null 表示身份不可用（已打 ERROR，调用方直接放弃接入）
     */
    private fun resolveIdentity(
        credentialsStore: AgentCredentialsStore,
        endpointReport: EndpointReport,
        capacity: Int,
    ): AgentIdentity? {
        val identityStore = AgentIdentityStore(getDataFolder().toPath())
        // 只读不新建：旧实现走 loadOrCreate()，文件缺失时会落盘一个从未在控制面注册过的新 UUID。
        // 凭据才是身份真源，那个 UUID 只会在凭据变得不可读时被迁移「复活」成一台新服务器。
        val storedIdentity = identityStore.loadExisting()
        // 只有「文件存在但读不懂」才是真损坏，须 fail-fast 让人看见。
        // 「文件不存在」是全新安装的正常态：此时凭据可能也还没有（等 join），
        // 若也按损坏处理，就会把「还没接入」误报成「身份坏了」并把零配置接入直接堵死。
        if (storedIdentity.fileExists && !storedIdentity.isValid) {
            severe("身份文件损坏：${storedIdentity.error}，Beacon agent 不接入控制面")
            return null
        }
        // 凭据 > 旧 identity.yml（迁移一次） > 新生成并落盘。
        val identityId = credentialsStore.resolveIdentityId(storedIdentity)
        // 把 identity.yml 对齐成权威身份：两份文件从此永远指向同一个 ID，
        // 历史上残留的休眠 UUID 就地纠正，迁移路径再也不会把它复活（取舍见 alignTo 注释）。
        identityStore.alignTo(identityId)
        // 服务器工作目录（FR-226）：agent dataFolder 的父（plugins）的父 = 服务器根，与 FR-163 扫描根一致。
        val serverWorkDir = getDataFolder().absoluteFile.parentFile?.parentFile?.absolutePath.orEmpty()
        // 角色按壳固定为 bukkit；agent 构建版本经 TabooLib pluginVersion 注入（FR-86，见 ADR-0039）。
        return AgentBootstrap.readIdentity(role = "bukkit", agentVersion = pluginVersion, serverWorkDir = serverWorkDir)
            .copy(
                identityId = identityId,
                bootId = UUID.randomUUID().toString(),
                endpointReport = endpointReport,
                capacity = capacity,
            )
    }

    private fun startActiveRuntime(
        identity: AgentIdentity,
        binding: ActiveBinding,
        deps: ActiveRuntimeDeps,
    ) {
        val assembled =
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
                        // 运行指标供给（FR-32 / FR-144）：内存 / CPU 现采，在线 / TPS 取自主线程原子埋点（不在采样线程调 Bukkit API）。
                        metricsProvider = {
                            BukkitMetricsCollector.sample(
                                deps.instrumentation.currentTps(),
                                deps.instrumentation.onlineCount(),
                            )
                        },
                        // 自我保护：把本壳 plugin 名注入 applier 作受保护顶段，命中即跳过——杜绝运维误把
                        // plugins/BeaconAgent/* 经 FR-14 文件树或 FR-38 导入塞进有效树后覆写自身（与 FR-41 env 注入身份呼应）。
                        selfPluginDirNames = setOf("BeaconAgent"),
                        authorityInvalidated = {
                            deps.snapshots.invalidate()
                            stopActiveRuntime()
                        },
                    ),
            )
        lifecycle = assembled.lifecycle
        // 跨服消息模块（FR-149，HTTP 中转）：随注册成功自启（AgentAssembly 已挂 onRegistered），此处仅留引用供 DISABLE 停止。
        messagingRuntime = assembled.messaging.messagingRuntime
        // 玩家名册持有者（FR-31）：装配期已随 Discovery 门面创建，此处留引用供 DISABLE 复位。
        rosterDirectoryHolder = assembled.messaging.rosterDirectoryHolder
        assembled.lifecycle.onRegistered { deps.snapshots.write(identity, binding) }
        assembled.lifecycle.onRegistered { deps.instrumentation.start() }
        // 玩家名册（FR-31）：注册成功后才注入控制面 HTTP 名册适配器——端点按 v2 鉴权身份圈定 namespace，
        // 未注册时请求必被 401 拒绝，提前注入没有意义（注入的是「已确认绑定」的身份，与此处 identity 一致）。
        assembled.lifecycle.onRegistered {
            assembled.messaging.rosterDirectoryHolder.set(HttpRosterDirectory(assembled.apiClient, identity))
        }

        // 对外注册门面，供同进程业务插件读取。
        BeaconAgentProvider.register(assembled.beaconAgent)

        // 注册本地运维命令 /beacon（status/reload/reconnect/resync）。
        BeaconAgentCommand.register(assembled.lifecycle, deps.adapter)

        // 启用 v2 指标 1s 采样 + 5s 批上报（FR-144）：须在接入前开启，注册成功即启两条循环。
        assembled.lifecycle.enableMetricsSampling()

        // 先点亮快照再异步接入，不阻塞主线程，不阻断玩家进服。
        assembled.lifecycle.bootstrapWithSnapshotThenConnect()
    }

    @Awake(LifeCycle.DISABLE)
    fun disable() {
        bootstrapRuntime?.shutdown()
        stopActiveRuntime()
    }

    /** 控制面撤销与插件卸载共用的 active 资源停止入口。 */
    private fun stopActiveRuntime() {
        messagingRuntime?.stop()
        messagingRuntime = null
        // 名册复位为未注入：停用后本机身份已失效（读取只会 401），持旧引用无意义，复位即让业务插件走降级。
        rosterDirectoryHolder?.reset()
        rosterDirectoryHolder = null
        lifecycle?.shutdown()
        lifecycle = null
        tickInstrumentation?.stop()
        tickInstrumentation = null
        bootstrapRuntime?.shutdown()
        bootstrapRuntime = null
        BeaconAgentProvider.unregister()
    }

    /** 本壳不硬链 Bukkit API，读取导出静态 API 的唯一实际监听端口。 */
    private fun readListenPort(): Int? =
        try {
            (Class.forName("org.bukkit.Bukkit").getMethod("getPort").invoke(null) as? Number)?.toInt()
        } catch (e: ReflectiveOperationException) {
            warning("读取 Bukkit 监听端口失败，等待控制面按连接事实补全：${e.message}")
            null
        }

    /**
     * 主线程反射读 Paper `max-players`（FR-228）：作为容量上限上报，使健康 `capacity` 因子可参与打分。
     * 取不到（非 Paper / API 变更）回 0——控制面据此判因子「不适用」，不报错、不阻断接入。
     */
    private fun readMaxPlayers(): Int =
        try {
            (Class.forName("org.bukkit.Bukkit").getMethod("getMaxPlayers").invoke(null) as? Number)?.toInt() ?: 0
        } catch (e: ReflectiveOperationException) {
            warning("读取 Bukkit max-players 失败，capacity 因子将不适用：${e.message}")
            0
        }
}
