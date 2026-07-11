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
import taboolib.module.configuration.Config
import taboolib.module.configuration.Configuration
import top.wcpe.beacon.agent.adapters.KotlinxJsonCodec
import top.wcpe.beacon.agent.adapters.OkHttpStreamTransport
import top.wcpe.beacon.agent.adapters.OkHttpTransport
import top.wcpe.beacon.agent.api.BeaconAgentProvider
import top.wcpe.beacon.agent.core.AgentAssembly
import top.wcpe.beacon.agent.core.AssembledAgent
import top.wcpe.beacon.agent.core.api.EffectiveConfigView
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.identity.AgentIdentityStore
import top.wcpe.beacon.agent.core.lifecycle.AgentLifecycle
import top.wcpe.beacon.agent.core.settings.AgentBootstrap
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.EnvOverridingConfigReader
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
    // Redis 客户端（FR-26 跨服消息中间件）：运行期下载、relocate 到隔离命名空间、不打包、不经 CoreLib。
    // Jedis 是纯 Java 库，传递依赖（commons-pool2 / gson / slf4j）手动列全（transitive=false）。
    // 关键：TabooLib 的 relocate 按依赖各自的 jar 生效，故 jedis 这条必须把它内部引用、且被本工程同样 relocate 的
    // 传递依赖（commons-pool2 / gson）一并声明 relocate，否则下载并重定位后的 jedis 仍引用原始包名
    // org.apache.commons.pool2.* / com.google.gson.*，而类路径只有重定位副本（lib.*）→ 运行期
    // NoClassDefFoundError（如 JedisPoolConfig 继承 org.apache.commons.pool2.impl.GenericObjectPoolConfig）。
    // slf4j 不在此列：由平台（Paper/Bungee）提供，保持原始包名解析，不重定位。
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
object BeaconAgentBukkit : Plugin() {
    /** agent 引导配置（资源 config.yml 随 jar 释放到数据目录）。 */
    @Config("config.yml")
    lateinit var config: Configuration

    /** 当前生命周期；null 表示因身份缺失未启动。 */
    private var lifecycle: AgentLifecycle? = null

    /** 主线程指标埋点（FR-144）；null 表示未启动（身份缺失等）。 */
    private var tickInstrumentation: BukkitTickInstrumentation? = null

    /** 跨服消息模块引导（FR-26）；null 表示未装配（身份缺失等）。 */
    private var messagingBootstrap: BukkitMessagingBootstrap? = null

    @Awake(LifeCycle.ENABLE)
    fun enable() {
        // 包一层环境变量覆盖（FR-33）：BEACON_AGENT_<点分路径大写、点/连字符转下划线> 优先于 config.yml。
        val reader = EnvOverridingConfigReader(TabooLibConfigReader(config), System::getenv)
        val settings = AgentBootstrap.readSettings(reader)
        submitAsync {
            val storedIdentity = AgentIdentityStore(getDataFolder().toPath()).loadOrCreate()
            // 角色按壳固定为 bukkit；agent 构建版本经 TabooLib pluginVersion 注入（FR-86，见 ADR-0039）。
            val identity =
                AgentBootstrap.readIdentity(reader, role = "bukkit", agentVersion = pluginVersion)
                    .copy(identityId = storedIdentity.identityId, bootId = UUID.randomUUID().toString())

            // fail-fast：身份缺失则打 ERROR 且不启循环（不阻断服务器，仅 agent 不接入）。
            var canConnect = true
            if (!storedIdentity.isValid) {
                severe("身份文件损坏：${storedIdentity.error}，Beacon agent 不接入控制面")
                canConnect = false
            }
            if (canConnect && !identity.isValid()) {
                severe("身份缺失：identity.serverId 与 identity.namespace 必须显式配置，Beacon agent 不接入控制面")
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
            instrumentation.start()
            tickInstrumentation = instrumentation

            // 装配：先建 store + view，再用 view 构 adapter（adapter 在变更时回调 view 派发 API 监听器）。
            val store = EffectiveConfigStore()
            val view = EffectiveConfigView(store)
            val adapter = BukkitPlatformAdapter(view)
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
                    // 运行指标供给（FR-32 / FR-144）：内存 / CPU 现采，在线 / TPS 取自主线程原子埋点（不在采样线程调 Bukkit API）。
                    metricsProvider = { BukkitMetricsCollector.sample(instrumentation.currentTps(), instrumentation.onlineCount()) },
                    // 自我保护：把本壳 plugin 名注入 applier 作受保护顶段，命中即跳过——杜绝运维误把
                    // plugins/BeaconAgent/* 经 FR-14 文件树或 FR-38 导入塞进有效树后覆写自身（与 FR-41 env 注入身份呼应）。
                    selfPluginDirNames = setOf("BeaconAgent"),
                )
            lifecycle = assembled.lifecycle

            // 对外注册门面，供同进程业务插件读取。
            BeaconAgentProvider.register(assembled.beaconAgent)

            // 注册本地运维命令 /beacon（status/reload/reconnect/resync）。
            BeaconAgentCommand.register(assembled.lifecycle, adapter)

            // 跨服消息模块引导（FR-26）：据下发的 Redis 配置启停 / 重连。
            val bootstrap = createMessaging(identity, settings, store, assembled, adapter)
            messagingBootstrap = bootstrap
            // 配置变更后重算消息模块状态（Redis 连接随有效配置下发，决策 15）。
            view.onChange { _, _ -> bootstrap.sync() }

            // 启用 v2 指标 1s 采样 + 5s 批上报（FR-144）：须在接入前开启，注册成功即启两条循环。
            assembled.lifecycle.enableMetricsSampling()

            // 先点亮快照再异步接入，不阻塞主线程，不阻断玩家进服。
            assembled.lifecycle.bootstrapWithSnapshotThenConnect()
            // 快照可能已含 Redis 配置：立即尝试一次（缺失则保持降级，待配置下发再起）。
            bootstrap.sync()
        }
    }

    /** 构造跨服消息模块引导（FR-26）：抽出以精简 enable 主流程；名册只读端口持有者随之注入（FR-31）。 */
    private fun createMessaging(
        identity: AgentIdentity,
        settings: AgentSettings,
        store: EffectiveConfigStore,
        assembled: AssembledAgent,
        adapter: BukkitPlatformAdapter,
    ): BukkitMessagingBootstrap =
        BukkitMessagingBootstrap(
            identity = identity,
            settings = settings,
            store = store,
            codec = KotlinxJsonCodec(),
            holder = assembled.messagingHolder,
            rosterHolder = assembled.rosterDirectoryHolder,
            adapter = adapter,
        )

    @Awake(LifeCycle.DISABLE)
    fun disable() {
        messagingBootstrap?.stop()
        lifecycle?.shutdown()
        tickInstrumentation?.stop()
        BeaconAgentProvider.unregister()
    }
}
