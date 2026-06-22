package top.wcpe.beacon.agent.bungee

import top.wcpe.beacon.agent.core.lifecycle.AgentLifecycle
import top.wcpe.beacon.agent.core.lifecycle.OpsCommandText
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import taboolib.common.platform.ProxyCommandSender
import taboolib.common.platform.command.command

/**
 * agent 本地运维命令 /beacon（权限 beacon.admin）：status / reload / reconnect / resync / help。
 *
 * 仅本地命令（FR-17，远程下发依赖鉴权 FR-11，本期不做）。命令体经 adapter.runAsync 落异步线程，
 * 不在代理主线程做阻塞动作；文案统一由 core 的 OpsCommandText 提供。
 *
 * 帮助 / 错参提示（FR-54）：补 help 子命令、无参打印用法、未知子命令 / 错参经 incorrectCommand 回中文用法
 * （取代 TabooLib 默认的中英双语 generic 提示），各 literal 带 description 供补全与帮助发现。
 */
object BeaconAgentCommand {

    /** 注册根命令；在壳层装配完成、确有 lifecycle 后调用。 */
    fun register(lifecycle: AgentLifecycle, adapter: PlatformAdapter) {
        command("beacon", permission = "beacon.admin") {
            literal("status", description = "查看 agent 接入与有效配置状态") {
                execute<ProxyCommandSender> { sender, _, _ ->
                    // status 读内存快照，开销极小，可直接回；为统一仍下沉异步。
                    adapter.runAsync {
                        OpsCommandText.statusLines(lifecycle.snapshot()).forEach { sender.sendMessage(it) }
                    }
                }
            }
            literal("reload", description = "强制立刻重拉有效配置并应用") {
                execute<ProxyCommandSender> { sender, _, _ ->
                    lifecycle.forcePollNow()
                    sender.sendMessage(OpsCommandText.RELOAD_TRIGGERED)
                }
            }
            literal("reconnect", description = "打断退避、重置并重新接入控制面") {
                execute<ProxyCommandSender> { sender, _, _ ->
                    lifecycle.reconnectNow()
                    sender.sendMessage(OpsCommandText.RECONNECT_TRIGGERED)
                }
            }
            literal("resync", description = "强制立刻重新同步文件树（需开启 file-tree.enabled）") {
                execute<ProxyCommandSender> { sender, _, _ ->
                    // forceSyncFileTreeNow 内部即转异步，仅返回是否已触发，不阻塞代理主线程。
                    val triggered = lifecycle.forceSyncFileTreeNow()
                    sender.sendMessage(OpsCommandText.resyncReply(triggered))
                }
            }
            literal("help", description = "查看各子命令用法") {
                execute<ProxyCommandSender> { sender, _, _ ->
                    OpsCommandText.HELP_LINES.forEach { sender.sendMessage(it) }
                }
            }
            // 无子命令：打印用法。
            execute<ProxyCommandSender> { sender, _, _ ->
                OpsCommandText.USAGE_LINES.forEach { sender.sendMessage(it) }
            }
            // 未知子命令 / 错参：回中文用法（带未知片段回显），取代 TabooLib 默认中英双语 generic 提示。
            // 取触发失配的输入片段经 self()（公共入口）；极端边界取不到则只给用法、不强求回显。
            incorrectCommand { sender, context, _, _ ->
                val input = runCatching { context.self() }.getOrNull()
                OpsCommandText.incorrectInputLines(input).forEach { sender.sendMessage(it) }
            }
        }
    }
}
