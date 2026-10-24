package top.wcpe.beacon.e2e

import org.bukkit.Bukkit
import org.bukkit.event.EventHandler
import org.bukkit.event.EventPriority
import org.bukkit.event.Listener
import org.bukkit.event.player.AsyncPlayerChatEvent
import org.bukkit.event.player.PlayerJoinEvent
import org.bukkit.plugin.java.JavaPlugin
import taboolib.common.platform.function.info
import taboolib.common.platform.function.submit
import java.io.File
import java.nio.file.Files
import java.nio.file.StandardCopyOption
import java.util.concurrent.atomic.AtomicBoolean

/**
 * FR-282 子服关闭回退场景的桩侧驱动（Bukkit 壳）。
 *
 * 三台后端（game / lobby1 / lobby2）都跑这份桩，靠环境变量 `BEACON_E2E_RECONNECT_ROLE`
 * 区分自己是谁。协议（冻结，机器人侧 `reconnect-bot` 对齐同一组标记）：
 *
 *  1. 玩家进服 → 桩在聊天里发 `BEACON_E2E_READY:<role>`，告诉 bot 自己落在了哪台后端；
 *  2. bot 发 `BEACON_E2E_TRIGGER_CLOSE` → **仅 game 角色**响应，调度一次真实关服
 *     （`Bukkit.shutdown()`），模拟「业务子服关闭」，从而触发代理侧的 ServerKickEvent；
 *  3. bot 回退成功后发 `BEACON_E2E_LANDED:<role>` → 收到的那台桩把自己的 role 记进结果文件。
 *
 * **结果文件遵守 mc-testkit 契约（docs/API.md §3.6）**：写到编排经
 * `MC_TESTKIT_E2E_RESULT_FILE` 下发的绝对路径，内容为 `status` / `message` 等键，
 * 且必须原子落盘（同目录临时文件 + rename 替换），避免 verify 读到写了一半的文件。
 *
 * 为什么关服用 `Bukkit.shutdown()` 而不是杀进程：本桩运行在框架拉起的后端里，
 * 只有服务端自己知道如何优雅停机；杀进程会连带 mc-testkit 的收尾台账失准。
 */
object ReconnectE2EProbe : Listener {
    /** 协议标记：后端就绪（带 role 后缀）。 */
    private const val READY_MARKER = "BEACON_E2E_READY"

    /** 协议标记：请求关闭当前后端。 */
    private const val TRIGGER_CLOSE_MARKER = "BEACON_E2E_TRIGGER_CLOSE"

    /** 协议标记：回退落点上报（带 role 后缀）。 */
    private const val LANDED_MARKER = "BEACON_E2E_LANDED"

    /** 编排下发的场景 id；本桩只在该场景下判定，其它场景空闲。 */
    private const val SCENARIO_ID = "reconnect"

    /** 关服只能触发一次，避免重复标记把后端反复拉起又关掉。 */
    private val closeRequested = AtomicBoolean(false)

    /** 本桩扮演的后端角色（game / lobby-1 / lobby-2）；空则本探针不启用。 */
    private val role: String = System.getenv("BEACON_E2E_RECONNECT_ROLE")?.trim().orEmpty()

    /** 编排实际跑的场景（serve 手测场景下不判定）。 */
    private val scenario: String = System.getenv("MC_TESTKIT_E2E_SCENARIO")?.trim().orEmpty()

    /** 结果文件绝对路径（编排 verify 的读取处）。 */
    private val resultFile: String = System.getenv("MC_TESTKIT_E2E_RESULT_FILE")?.trim().orEmpty()

    fun start() {
        if (role.isBlank()) {
            info("未配置 BEACON_E2E_RECONNECT_ROLE，跳过 FR-282 回退探针")
            return
        }
        val plugin = Bukkit.getPluginManager().getPlugin("BeaconE2E") ?: JavaPlugin.getProvidingPlugin(javaClass)
        Bukkit.getPluginManager().registerEvents(this, plugin)
        info("Beacon E2E 回退探针已启用，role=$role，scenario=$scenario，结果文件=$resultFile")
    }

    /** 玩家进服：通告本后端身份，让 bot 知道自己落在哪台。 */
    @EventHandler(priority = EventPriority.MONITOR)
    fun onJoin(event: PlayerJoinEvent) {
        // 延迟 1 tick：确保客户端已完全进入世界、能收到聊天消息。
        submit(delay = 1L) {
            event.player.chat("$READY_MARKER:$role")
        }
    }

    /** 聊天协议：触发关服 / 上报落点。 */
    @EventHandler(priority = EventPriority.MONITOR, ignoreCancelled = true)
    fun onChat(event: AsyncPlayerChatEvent) {
        // 只在回退场景下参与判定；serve 手测或其它场景一律空闲，避免误关服。
        if (scenario.isNotBlank() && scenario != SCENARIO_ID) return

        val text = event.message.trim()
        when {
            // 只有 game 角色响应关闭请求：大厅服不该被这个场景关掉。
            text == TRIGGER_CLOSE_MARKER && role == "game" -> {
                if (!closeRequested.compareAndSet(false, true)) return
                info("收到关闭请求，2 秒后关停本后端（role=$role）")
                // 延迟 2 秒：给 bot 的"断线后重连"登记、以及代理感知留出窗口。
                submit(delay = 40L) {
                    Bukkit.shutdown()
                }
            }

            text.startsWith("$LANDED_MARKER:") -> {
                val landedRole = text.substringAfter(':').trim()
                // 由**收到落点上报的那台服**写结果——回退后玩家所在的就是它，
                // 而触发关服的那台（game）此刻已经停了，不可能写文件。
                writeResult(landedRole)
            }
        }
    }

    /**
     * 按 mc-testkit 契约写结果文件：`status` + `message` + 场景自有键，原子落盘。
     *
     * 判定口径（spec §6.2）：回退**落到了大厅**即视为通过——落到 game 自己说明没回退，
     * 落空说明玩家被踢回了登录界面。
     */
    private fun writeResult(landedRole: String) {
        val target = resultFile.takeIf { it.isNotBlank() }?.let(::File)
        if (target == null) {
            info("未收到 MC_TESTKIT_E2E_RESULT_FILE，跳过结果写入（手动 serve 模式）")
            return
        }
        val passed = landedRole.startsWith("lobby")
        val body =
            buildString {
                appendLine("status=${if (passed) "PASS" else "FAIL"}")
                appendLine("message=${if (passed) "回退成功：落到 $landedRole" else "回退落点异常：$landedRole"}")
                appendLine("landedOn=$landedRole")
                appendLine("servedBy=$role")
            }
        target.parentFile?.mkdirs()
        // 原子落盘：同目录临时文件 + rename，避免 verify 读到写了一半的内容。
        val tmp = File(target.parentFile, "${target.name}.tmp")
        tmp.writeText(body)
        Files.move(tmp.toPath(), target.toPath(), StandardCopyOption.REPLACE_EXISTING)
        info("已写入回退结果：landedOn=$landedRole（本服 role=$role）")
    }
}
