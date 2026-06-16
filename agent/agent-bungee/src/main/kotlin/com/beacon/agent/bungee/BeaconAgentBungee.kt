package com.beacon.agent.bungee

import com.beacon.agent.adapters.KotlinxJsonCodec
import com.beacon.agent.adapters.OkHttpTransport
import com.beacon.agent.api.BeaconAgentProvider
import com.beacon.agent.core.AgentAssembly
import com.beacon.agent.core.api.EffectiveConfigView
import com.beacon.agent.core.config.EffectiveConfigStore
import com.beacon.agent.core.lifecycle.AgentLifecycle
import com.beacon.agent.core.settings.AgentBootstrap
import taboolib.common.LifeCycle
import taboolib.common.platform.Awake
import taboolib.common.platform.Plugin
import taboolib.common.platform.function.severe
import taboolib.module.configuration.Config
import taboolib.module.configuration.Configuration

/**
 * BungeeCord 代理侧 Beacon agent 插件主类（object + @Awake，不继承 Plugin 基类外的内容）。
 *
 * ENABLE：读 config.yml → 构 AgentSettings + AgentIdentity（身份缺失 fail-fast）→
 *         装配 OkHttpTransport + KotlinxJsonCodec + Bungee 适配器 → bootstrap 接入。
 * DISABLE：停循环 + 注销门面。
 */
object BeaconAgentBungee : Plugin() {

    /** agent 引导配置（资源 config.yml 随 jar 释放到数据目录）。 */
    @Config("config.yml")
    lateinit var config: Configuration

    /** 当前生命周期；null 表示因身份缺失未启动。 */
    private var lifecycle: AgentLifecycle? = null

    @Awake(LifeCycle.ENABLE)
    fun enable() {
        val reader = TabooLibConfigReader(config)
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

        // 先点亮快照再异步接入，不阻塞主线程。
        assembled.lifecycle.bootstrapWithSnapshotThenConnect()
    }

    @Awake(LifeCycle.DISABLE)
    fun disable() {
        lifecycle?.shutdown()
        BeaconAgentProvider.unregister()
    }
}
