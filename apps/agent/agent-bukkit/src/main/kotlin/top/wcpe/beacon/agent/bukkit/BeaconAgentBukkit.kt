package top.wcpe.beacon.agent.bukkit

import taboolib.common.LifeCycle
import taboolib.common.env.RuntimeDependencies
import taboolib.common.env.RuntimeDependency
import taboolib.common.platform.Awake
import taboolib.common.platform.Plugin
import taboolib.common.platform.function.getDataFolder
import taboolib.common.platform.function.pluginVersion
import taboolib.common.platform.function.severe
import taboolib.common.platform.function.submitAsync
import taboolib.common.platform.function.warning
import taboolib.module.configuration.Config
import taboolib.module.configuration.Configuration
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
import top.wcpe.beacon.agent.core.identity.AgentIdentityStore
import top.wcpe.beacon.agent.core.identity.EndpointReport
import top.wcpe.beacon.agent.core.identity.IdentityBindingSnapshotStore
import top.wcpe.beacon.agent.core.lifecycle.AgentLifecycle
import top.wcpe.beacon.agent.core.lifecycle.BootstrapRuntime
import top.wcpe.beacon.agent.core.messaging.MessagingRuntime
import top.wcpe.beacon.agent.core.settings.AgentBootstrap
import top.wcpe.beacon.agent.core.settings.EnvOverridingConfigReader
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

    /** 当前生命周期；null 表示因身份缺失未启动。 */
    private var lifecycle: AgentLifecycle? = null

    /** 待确认阶段仅运行此最小引导器，绝不提前创建数据面组件。 */
    private var bootstrapRuntime: BootstrapRuntime? = null

    /** 主线程指标埋点（FR-144）；null 表示未启动（身份缺失等）。 */
    private var tickInstrumentation: BukkitTickInstrumentation? = null

    /** 跨服消息模块运行时（FR-149，HTTP 中转）；null 表示未装配。随注册自启，DISABLE 时 stop。 */
    private var messagingRuntime: MessagingRuntime? = null

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
        // 包一层环境变量覆盖（FR-33）：BEACON_AGENT_<点分路径大写、点/连字符转下划线> 优先于 config.yml。
        val reader = EnvOverridingConfigReader(TabooLibConfigReader(config), System::getenv)
        val settings = AgentBootstrap.readSettings(reader)
        val endpointReport = EndpointReport(backendListenPort = readListenPort())
        submitAsync {
            val storedIdentity = AgentIdentityStore(getDataFolder().toPath()).loadOrCreate()
            // 服务器工作目录（FR-226）：agent dataFolder 的父（plugins）的父 = 服务器根，与 FR-163 扫描根一致。
            val serverWorkDir = getDataFolder().absoluteFile.parentFile?.parentFile?.absolutePath.orEmpty()
            // 角色按壳固定为 bukkit；agent 构建版本经 TabooLib pluginVersion 注入（FR-86，见 ADR-0039）。
            val identity =
                AgentBootstrap.readIdentity(role = "bukkit", agentVersion = pluginVersion, serverWorkDir = serverWorkDir)
                    .copy(identityId = storedIdentity.identityId, bootId = UUID.randomUUID().toString(), endpointReport = endpointReport)

            // fail-fast：身份缺失则打 ERROR 且不启循环（不阻断服务器，仅 agent 不接入）。
            var canConnect = true
            if (!storedIdentity.isValid) {
                severe("身份文件损坏：${storedIdentity.error}，Beacon agent 不接入控制面")
                canConnect = false
            }
            if (canConnect && (settings.endpoints.isEmpty() || settings.bootstrapToken.isBlank())) {
                severe("配置缺失：beacon.endpoints 与 beacon.bootstrapToken 必填，Beacon agent 不接入控制面")
                canConnect = false
            }
            if (!canConnect) {
                return@submitAsync
            }

            // 主线程指标埋点（FR-144）：MC 主线程每 tick 零成本埋点（tick 计数 / 在线 volatile），
            // 采样 / 上报线程只读 volatile 推算，绝不在别的线程调线程不安全的 Bukkit API。
            val instrumentation = BukkitTickInstrumentation()
            tickInstrumentation = instrumentation

            // 装配：先建 store + view，再用 view 构 adapter（adapter 在变更时回调 view 派发 API 监听器）。
            val store = EffectiveConfigStore()
            val view = EffectiveConfigView(store)
            val adapter = BukkitPlatformAdapter(view)
            val codec = KotlinxJsonCodec()
            val bindingSnapshot = IdentityBindingSnapshotStore(File(getDataFolder(), "identity-binding.snapshot.json"), codec)
            val bootstrapClient = BeaconApiClient(OkHttpTransport(connectTimeoutMs = settings.requestTimeoutMs), codec, settings)
            val bootstrap =
                BootstrapRuntime(
                    identity = identity,
                    settings = settings,
                    adapter = adapter,
                    apiClient = bootstrapClient,
                    snapshots = bindingSnapshot,
                    onActive = { activeIdentity, binding ->
                        startActiveRuntime(
                            identity = activeIdentity,
                            binding = binding,
                            deps = ActiveRuntimeDeps(settings, adapter, codec, store, view, instrumentation, bindingSnapshot),
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

    private fun startActiveRuntime(
        identity: top.wcpe.beacon.agent.core.identity.AgentIdentity,
        binding: top.wcpe.beacon.agent.core.client.ActiveBinding,
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
        assembled.lifecycle.onRegistered { deps.snapshots.write(identity, binding) }
        assembled.lifecycle.onRegistered { deps.instrumentation.start() }

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
}
