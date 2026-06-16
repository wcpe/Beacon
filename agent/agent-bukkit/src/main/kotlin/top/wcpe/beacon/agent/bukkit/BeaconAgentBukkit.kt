package top.wcpe.beacon.agent.bukkit

import top.wcpe.beacon.agent.adapters.KotlinxJsonCodec
import top.wcpe.beacon.agent.adapters.OkHttpTransport
import top.wcpe.beacon.agent.api.BeaconAgentProvider
import top.wcpe.beacon.agent.core.AgentAssembly
import top.wcpe.beacon.agent.core.api.EffectiveConfigView
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.lifecycle.AgentLifecycle
import top.wcpe.beacon.agent.core.settings.AgentBootstrap
import taboolib.common.LifeCycle
import taboolib.common.env.RuntimeDependencies
import taboolib.common.env.RuntimeDependency
import taboolib.common.platform.Awake
import taboolib.common.platform.Plugin
import taboolib.common.platform.function.severe
import taboolib.module.configuration.Config
import taboolib.module.configuration.Configuration

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

    @Awake(LifeCycle.ENABLE)
    fun enable() {
        val reader = TabooLibConfigReader(config)
        val settings = AgentBootstrap.readSettings(reader)
        // 角色按壳固定为 bukkit。
        val identity = AgentBootstrap.readIdentity(reader, role = "bukkit")

        // fail-fast：身份缺失则打 ERROR 且不启循环（不阻断服务器，仅 agent 不接入）。
        if (!identity.isValid()) {
            severe("身份缺失：identity.serverId 与 identity.namespace 必须显式配置，Beacon agent 不接入控制面")
            return
        }
        if (settings.endpoints.isEmpty() || settings.bootstrapToken.isBlank()) {
            severe("配置缺失：beacon.endpoints 与 beacon.bootstrapToken 必填，Beacon agent 不接入控制面")
            return
        }

        // 装配：先建 store + view，再用 view 构 adapter（adapter 在变更时回调 view 派发 API 监听器）。
        val store = EffectiveConfigStore()
        val view = EffectiveConfigView(store)
        val adapter = BukkitPlatformAdapter(view)
        val assembled = AgentAssembly.assemble(
            identity = identity,
            settings = settings,
            adapter = adapter,
            transport = OkHttpTransport(connectTimeoutMs = settings.requestTimeoutMs),
            codec = KotlinxJsonCodec(),
            store = store,
            effectiveConfigView = view,
        )
        lifecycle = assembled.lifecycle

        // 对外注册门面，供同进程业务插件读取。
        BeaconAgentProvider.register(assembled.beaconAgent)

        // 先点亮快照再异步接入，不阻塞主线程，不阻断玩家进服。
        assembled.lifecycle.bootstrapWithSnapshotThenConnect()
    }

    @Awake(LifeCycle.DISABLE)
    fun disable() {
        lifecycle?.shutdown()
        BeaconAgentProvider.unregister()
    }
}
