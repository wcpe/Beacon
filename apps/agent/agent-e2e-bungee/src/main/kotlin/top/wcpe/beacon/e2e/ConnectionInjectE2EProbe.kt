package top.wcpe.beacon.e2e

import taboolib.common.platform.function.getDataFolder
import taboolib.common.platform.function.info
import taboolib.common.platform.function.submit
import top.wcpe.beacon.agent.bungee.BungeeConnectionListener
import java.io.File
import java.time.OffsetDateTime
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicLong

/**
 * FR-145 连接明细采集端到端探针（BungeeCord 壳）。
 *
 * proxy 侧连接采集依赖真 BC 玩家事件（PostLogin/ServerConnected/PlayerDisconnect），harness 无真玩家可登入，
 * 故本探针改为**直接驱动 agent 已装配的真采集入口** `BungeeConnectionListener.tracker`（由 BeaconAgentProxy 在
 * ENABLE 时注入的真 [ProxyConnectionTracker]）——喂构造的 open/换服/close 事件，走真有界缓冲 →
 * 真 [ConnectionReportCoordinator] → 真 [BeaconApiClient.reportConnectionsBatch] → 真控制面 /connections/batch。
 * 即除「真玩家触发 BC 事件」这一段外，wire 与落库全由真实代码路径覆盖（真玩家登入段留真机）。
 *
 * 由环境变量 `BEACON_E2E_CONNINJECT` 门控（非空才启用）。全程 async，不上 BC 主线程。
 * 时序稳健性：agent 未 approve 时上报被控制面 403、事件保留缓冲，approve 后由上报循环补报——故只需注入一次，
 * 落库最终一致。为让 close 更新到同一会话行并算出正向时长，open 与 close 间隔一个观察窗（[CLOSE_DELAY_MS]）。
 */
object ConnectionInjectE2EProbe {
    /** 标记文件名：外部驱动据此确认注入活性（真正断言在控制面 conn_detail 落库侧）。 */
    private const val OBSERVATION_FILE = "e2e-conninject.log"

    /** 轮询周期（tick，20 tick/秒）：约每 2 秒推进一步注入状态机。 */
    private const val POLL_INTERVAL_TICKS = 40L

    /** open 与 close 的间隔（毫秒）：给出非零会话时长、并让 open 先行一轮上报。 */
    private const val CLOSE_DELAY_MS = 6_000L

    /** 注入的固定玩家 UUID（Go 侧按此 player_uuid 查 conn_detail 会话行）。 */
    private const val PLAYER_UUID = "0192e2e0-0000-7000-8000-00000000c0de"

    /** 注入的玩家名（≤16，契合列宽）。 */
    private const val PLAYER_NAME = "E2EConnBot"

    /** 注入的客户端地址（文档示例网段，非真内网）。 */
    private const val CLIENT_IP = "203.0.113.7"

    /** 注入的 MC 协议号（示意值）。 */
    private const val PROTOCOL_VERSION = 765

    /** 首个后端子服（会话首后端）。 */
    private const val BACKEND_A = "e2e-backend-a"

    /** 换服后的后端子服（触发一次 switch，末后端 = B、switchCount=1）。 */
    private const val BACKEND_B = "e2e-backend-b"

    /** 门控：环境注入非空才启用本探针。 */
    private val enabled: String = System.getenv("BEACON_E2E_CONNINJECT") ?: ""

    /** open（含换服）是否已注入。 */
    private val opened = AtomicBoolean(false)

    /** close 是否已注入。 */
    private val closed = AtomicBoolean(false)

    /** open 注入时刻（毫秒），用于把 close 延后一个观察窗。 */
    private val openedAtMs = AtomicLong(0)

    fun start() {
        if (enabled.isBlank()) {
            info("未配置 BEACON_E2E_CONNINJECT，跳过 FR-145 连接采集探针")
            return
        }
        val markFile = File(getDataFolder(), OBSERVATION_FILE)
        if (markFile.exists()) {
            markFile.delete()
        }
        info("Beacon E2E 连接采集探针已启用，注入玩家=$PLAYER_UUID，标记文件=${markFile.absolutePath}")
        submit(async = true, delay = POLL_INTERVAL_TICKS, period = POLL_INTERVAL_TICKS) {
            probeOnce(markFile)
        }
    }

    /** 一步：拿到真 tracker 后先注入 open + 换服，隔一个观察窗再注入 close。 */
    private fun probeOnce(markFile: File) {
        // 取 BeaconAgentProxy 在 ENABLE 时装配注入的真采集入口；未就绪则下一轮再试。
        val tracker = BungeeConnectionListener.tracker
        if (tracker == null) {
            append(markFile, "NO_TRACKER", "-")
            return
        }
        if (opened.compareAndSet(false, true)) {
            // open → 首后端 A → 换服 B（末后端=B、switchCount=1）。
            tracker.onConnect(PLAYER_UUID, PLAYER_NAME, CLIENT_IP, PROTOCOL_VERSION)
            tracker.onBackend(PLAYER_UUID, BACKEND_A)
            tracker.onBackend(PLAYER_UUID, BACKEND_B)
            openedAtMs.set(System.currentTimeMillis())
            append(markFile, "INJECTED_OPEN", "player=$PLAYER_UUID;firstBackend=$BACKEND_A;lastBackend=$BACKEND_B")
            return
        }
        if (!closed.get() && System.currentTimeMillis() - openedAtMs.get() >= CLOSE_DELAY_MS) {
            if (closed.compareAndSet(false, true)) {
                tracker.onDisconnect(PLAYER_UUID, "quit", null)
                append(markFile, "INJECTED_CLOSE", "player=$PLAYER_UUID;closeKind=quit")
            }
        }
    }

    /** 向标记文件追加一行观测：时间 | 来源 | 明细。 */
    @Synchronized
    private fun append(
        file: File,
        source: String,
        detail: String,
    ) {
        file.parentFile?.mkdirs()
        file.appendText("${OffsetDateTime.now()}|$source|$detail\n", Charsets.UTF_8)
    }
}
