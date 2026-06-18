package top.wcpe.beacon.agent.bungee

import top.wcpe.beacon.agent.core.lifecycle.AgentLifecycle
import top.wcpe.beacon.agent.core.lifecycle.OpsCommandText
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import taboolib.common.platform.ProxyCommandSender
import taboolib.common.platform.command.command

/**
 * agent 本地运维命令 /beacon（权限 beacon.admin）：status / reload / reconnect / resync。
 *
 * 仅本地命令（FR-17，远程下发依赖鉴权 FR-11，本期不做）。命令体经 adapter.runAsync 落异步线程，
 * 不在代理主线程做阻塞动作；文案统一由 core 的 OpsCommandText 提供。
 */
object BeaconAgentCommand {

    /** 注册根命令；在壳层装配完成、确有 lifecycle 后调用。 */
    fun register(lifecycle: AgentLifecycle, adapter: PlatformAdapter) {
        val usageLines = OpsCommandText.USAGE_LINES
        command("beacon", permission = "beacon.admin") {
            literal("status") {
                execute<ProxyCommandSender> { sender, _, _ ->
                    // status 读内存快照，开销极小，可直接回；为统一仍下沉异步。
                    adapter.runAsync {
                        OpsCommandText.statusLines(lifecycle.snapshot()).forEach { sender.sendMessage(it) }
                    }
                }
            }
            literal("reload") {
                execute<ProxyCommandSender> { sender, _, _ ->
                    lifecycle.forcePollNow()
                    sender.sendMessage(OpsCommandText.RELOAD_TRIGGERED)
                }
            }
            literal("reconnect") {
                execute<ProxyCommandSender> { sender, _, _ ->
                    lifecycle.reconnectNow()
                    sender.sendMessage(OpsCommandText.RECONNECT_TRIGGERED)
                }
            }
            literal("resync") {
                execute<ProxyCommandSender> { sender, _, _ ->
                    sender.sendMessage(OpsCommandText.RESYNC_UNAVAILABLE)
                }
            }
            // 无子命令：打印用法。
            execute<ProxyCommandSender> { sender, _, _ ->
                usageLines.forEach { sender.sendMessage(it) }
            }
        }
    }
}
