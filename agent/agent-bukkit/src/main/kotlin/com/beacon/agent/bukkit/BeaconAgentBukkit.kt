package com.beacon.agent.bukkit

import taboolib.common.LifeCycle
import taboolib.common.platform.Awake
import taboolib.common.platform.Plugin
import taboolib.common.platform.function.info

/**
 * Bukkit 子服侧 Beacon agent 插件主类。
 *
 * M0 仅占位：打通 TabooLib 生命周期，确保骨架可编译可加载。
 * 注册 / 心跳 / 配置长轮询 / fail-static 等数据面逻辑在 M5 实现。
 */
object BeaconAgentBukkit : Plugin() {

    @Awake(LifeCycle.ENABLE)
    fun enable() {
        // M0 占位：仅打印启用日志，无业务逻辑。
        info("Beacon agent（Bukkit）已启用 —— M0 骨架占位")
    }

    @Awake(LifeCycle.DISABLE)
    fun disable() {
        // M0 占位：仅打印停用日志，无资源需要清理。
        info("Beacon agent（Bukkit）已停用")
    }
}
