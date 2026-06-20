package top.wcpe.beacon.agent.bungee

import top.wcpe.beacon.agent.adapters.KotlinxJsonCodec
import top.wcpe.beacon.agent.adapters.OkHttpStreamTransport
import top.wcpe.beacon.agent.adapters.OkHttpTransport
import top.wcpe.beacon.agent.api.BeaconAgentProvider
import top.wcpe.beacon.agent.api.DiscoveryQuery
import top.wcpe.beacon.agent.core.AgentAssembly
import top.wcpe.beacon.agent.core.api.EffectiveConfigView
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.lifecycle.AgentLifecycle
import top.wcpe.beacon.agent.core.proxy.ProxyServerDirectorySyncer
import top.wcpe.beacon.agent.core.settings.AgentBootstrap
import top.wcpe.beacon.agent.core.settings.EnvOverridingConfigReader
import taboolib.common.LifeCycle
import taboolib.common.env.RuntimeDependencies
import taboolib.common.env.RuntimeDependency
import taboolib.common.platform.Awake
import taboolib.common.platform.Plugin
import taboolib.common.platform.function.severe
import taboolib.module.configuration.Config
import taboolib.module.configuration.Configuration
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
    RuntimeDependency(
        "!redis.clients:jedis:4.2.3",
        test = "!top.wcpe.beacon.agent.lib.redis.clients.jedis.Jedis",
        relocate = ["!redis.clients.jedis", "!top.wcpe.beacon.agent.lib.redis.clients.jedis"],
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

    /** 玩家位置名册引导（FR-26）；null 表示未装配。 */
    private var rosterBootstrap: BungeePlayerRosterBootstrap? = null

    @Awake(LifeCycle.ENABLE)
    fun enable() {
        // 包一层环境变量覆盖（FR-33）：BEACON_AGENT_<点分路径大写、点/连字符转下划线> 优先于 config.yml。
        val reader = EnvOverridingConfigReader(TabooLibConfigReader(config), System::getenv)
        val settings = AgentBootstrap.readSettings(reader)
        // 角色按壳固定为 bungee。
        val identity = AgentBootstrap.readIdentity(reader, role = "bungee")

        // fail-fast：身份缺失则打 ERROR 且不启循环（不阻断代理，仅 agent 不接入）。
        if (!identity.isValid()) {
            severe("身份缺失：identity.server-id 与 identity.namespace 必须显式配置，Beacon agent 不接入控制面")
            return
        }
        if (settings.endpoints.isEmpty() || settings.bootstrapToken.isBlank()) {
            severe("配置缺失：beacon.endpoints 与 beacon.bootstrap-token 必填，Beacon agent 不接入控制面")
            return
        }

        // 装配：先建 store + view，再用 view 构 adapter（adapter 在变更时回调 view 派发 API 监听器）。
        val store = EffectiveConfigStore()
        val view = EffectiveConfigView(store)
        val adapter = BungeePlatformAdapter(view)
        // 单一代理目录实例：同时供目录同步（注入子服）与后端归属上报（读当前后端集合，FR-36）。
        val serverDirectory = BungeeServerDirectory()
        val assembled = AgentAssembly.assemble(
            identity = identity,
            settings = settings,
            adapter = adapter,
            transport = OkHttpTransport(connectTimeoutMs = settings.requestTimeoutMs),
            codec = KotlinxJsonCodec(),
            store = store,
            effectiveConfigView = view,
            // 单条 SSE 推送流（FR-24）：取代配置/文件树/覆盖集三条长轮询，纯 HTTP 读流、无重型依赖。
            streamTransport = OkHttpStreamTransport(connectTimeoutMs = settings.requestTimeoutMs),
            // 运行指标供给（FR-32）：上报时采代理在线人数 + JVM 内存 / CPU 真值（代理无 TPS，恒 0）。
            metricsProvider = { BungeeMetricsCollector.sample() },
            // 后端归属供给（FR-36）：注册/上报时取本代理当前代理的后端子服 serverId 集合（仅 bc 填）。
            backendsProvider = { serverDirectory.backendServerIds().toList() },
        )
        lifecycle = assembled.lifecycle

        // 对外注册门面，供同进程业务插件读取。
        BeaconAgentProvider.register(assembled.beaconAgent)

        // 注册本地运维命令 /beacon（status/reload/reconnect/resync）。
        BeaconAgentCommand.register(assembled.lifecycle, adapter)

        val directorySyncer = ProxyServerDirectorySyncer(
            directory = serverDirectory,
            warn = { adapter.warn(it) },
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
        val roster = BungeePlayerRosterBootstrap(
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

        // 先点亮快照再异步接入，不阻塞主线程。
        assembled.lifecycle.bootstrapWithSnapshotThenConnect()
        // 快照可能已含 Redis 配置：立即尝试一次（缺失则空闲，待配置下发再起）。
        roster.sync()
    }

    private fun syncDirectoryLoop(adapter: BungeePlatformAdapter, syncer: ProxyServerDirectorySyncer) {
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
        rosterBootstrap?.stop()
        lifecycle?.shutdown()
        BeaconAgentProvider.unregister()
    }

    private const val DIRECTORY_SYNC_INTERVAL_MS = 10_000L
}
