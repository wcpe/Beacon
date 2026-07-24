package top.wcpe.beacon.e2e

import taboolib.common.platform.function.getDataFolder
import taboolib.common.platform.function.info
import taboolib.common.platform.function.submit
import top.wcpe.beacon.agent.api.BeaconAgentProvider
import java.io.File
import java.util.concurrent.atomic.AtomicBoolean

/**
 * FR-149 跨服消息门面端到端探针（Bukkit 壳）。
 *
 * 作为「业务插件」经 agent 的纯 Java 门面 `BeaconAgentProvider.get().messaging()` 真正走一遍
 * send / call / on 的收发闭环（HttpMessageTransport → 真控制面 /messages/send+poll+ack），把每步观测追加写到
 * 数据目录下 `e2e-messaging.log`，供外部 Go 驱动断言 wire 与落库：
 *  - 定向 send 自寻址（目标=本服 serverId）：控制面受理 → 长轮询取回 → on 处理器收到 → 回执 delivered；
 *  - RPC call 自寻址：请求 correlationId 自引用其 messageId、响应回填该 messageId，Future 往返完成；
 *  - 玩家寻址落空：向名册无此人的随机 UUID 发消息，控制面记 failed(player_not_online)；
 *  - 广播 publish/subscribe（FR-180）：控制面按在线服集合 fan-out（含发送者自身）→ 本机 subscribe 收到自身广播 →
 *    回执 delivered，一条广播只落一行聚合 msg_trace（target_kind=broadcast）。
 *
 * 由环境变量 `BEACON_E2E_MESSAGING` 门控（非空才启用；随消息模块开关 messaging.enabled 一并由 servePaper 注入）。
 * 全程 async 线程，绝不上 MC 主线程；send/call 前置判 isAvailable 优雅降级。
 *
 * 时序稳健性：agent 注册成功即 isAvailable=true，但此刻身份可能尚未 approve，上行会被控制面 403 丢弃。
 * 故 send 每轮重发直至本机 on 收到自寻址消息为止（approve 后一条即成功、随即停发）；call 用单飞守卫，
 * 一次在途、完成即停——避免 approve 前的无谓请求在落库侧留多余行。
 */
object MessagingE2EProbe {
    /** 标记文件名：外部驱动据此断言。 */
    private const val OBSERVATION_FILE = "e2e-messaging.log"

    /** 轮询周期（tick，20 tick/秒）：约每 2 秒驱动一次收发。 */
    private const val POLL_INTERVAL_TICKS = 40L

    /** 定向 send 的业务消息类型（Go 侧按此 msgType 查 msg_trace）。 */
    private const val TYPE_MSG = "beacon-e2e-msg"

    /** RPC call 的业务消息类型。 */
    private const val TYPE_RPC = "beacon-e2e-rpc"

    /** 玩家寻址落空的业务消息类型（独立类型，便于 Go 侧无歧义查其 failed 行）。 */
    private const val TYPE_MISS = "beacon-e2e-miss"

    /** 广播 publish/subscribe 的业务消息类型（Go 侧按此 msgType 查 msg_trace 广播聚合行，FR-180）。 */
    private const val TYPE_BCAST = "beacon-e2e-bcast"

    /** 定向 send 的固定 payload 标记（字符串，契合 wire 的 payload 字符串形态）。 */
    private const val MSG_PAYLOAD = "beacon-e2e-hello"

    /** RPC 请求 payload 标记。 */
    private const val RPC_REQUEST = "beacon-e2e-ping"

    /** RPC 响应 payload 前缀（handler 回信 = 该前缀 + 请求 payload）。 */
    private const val RPC_REPLY_PREFIX = "pong:"

    /** 广播 publish 的固定 payload 标记。 */
    private const val BCAST_PAYLOAD = "beacon-e2e-broadcast"

    /** 玩家寻址落空用的随机玩家 UUID（名册绝无此人 → 控制面判 player_not_online）。 */
    private const val MISSING_PLAYER_UUID = "00000000-0000-7000-8000-0000e2e0dead"

    /** 门控：环境注入非空才启用本探针。 */
    private val enabled: String = System.getenv("BEACON_E2E_MESSAGING") ?: ""

    /** 处理器只注册一次。 */
    private val handlersRegistered = AtomicBoolean(false)

    /** 本机 on 是否已收到自寻址的定向消息（收到即停发）。 */
    private val msgReceived = AtomicBoolean(false)

    /** RPC 是否已往返完成（完成即停发）。 */
    private val rpcDone = AtomicBoolean(false)

    /** RPC 单飞：一次在途，避免 approve 前无谓请求叠加落库多余行。 */
    private val rpcInFlight = AtomicBoolean(false)

    /** 玩家寻址落空是否已发过一次（只发一次，控制面即记 failed）。 */
    private val playerMissSent = AtomicBoolean(false)

    /** 本机 subscribe 是否已收到自身广播（含自身语义，收到即停发）。 */
    private val bcastReceived = AtomicBoolean(false)

    fun start() {
        if (enabled.isBlank()) {
            info("未配置 BEACON_E2E_MESSAGING，跳过 FR-149 消息探针")
            return
        }
        val markFile = File(getDataFolder(), OBSERVATION_FILE)
        // 清空上轮残留，保证每次 run 的标记文件只含本轮观测。
        if (markFile.exists()) {
            markFile.delete()
        }
        info("Beacon E2E 消息探针已启用，标记文件=${markFile.absolutePath}")
        submit(async = true, delay = POLL_INTERVAL_TICKS, period = POLL_INTERVAL_TICKS) {
            probeOnce(markFile)
        }
    }

    /** 一轮：注册处理器（一次）→ 重发定向 send 直至收到 → 单飞发 RPC → 一次性发玩家寻址落空。 */
    private fun probeOnce(markFile: File) {
        if (!BeaconAgentProvider.isAvailable()) {
            return
        }
        val agent = BeaconAgentProvider.get()
        val messaging = agent.messaging()
        if (!messaging.isAvailable()) {
            return
        }
        val self = agent.identity().serverId()
        registerHandlers(markFile, messaging)

        // 定向 send 自寻址：approve 前会被控制面 403 丢弃（无落库行），故每轮重发直至本机 on 收到为止。
        if (!msgReceived.get()) {
            try {
                messaging.send(self, TYPE_MSG, MSG_PAYLOAD)
            } catch (t: Throwable) {
                E2EObservation.append(markFile, "SEND_ERROR", TYPE_MSG, "-", "err=${t.message}")
            }
        }

        // 广播 publish 含自身语义（FR-180）：approve 前会被控制面 403 丢弃（无落库行），故每轮重发直至本机
        // subscribe 收到自身广播为止（approve 后一条即 fan-out 回自身、随即停发）。
        if (!bcastReceived.get()) {
            try {
                messaging.publish(TYPE_BCAST, BCAST_PAYLOAD)
            } catch (t: Throwable) {
                E2EObservation.append(markFile, "PUBLISH_ERROR", TYPE_BCAST, "-", "err=${t.message}")
            }
        }

        // RPC call 自寻址：单飞、完成即停。
        if (!rpcDone.get() && rpcInFlight.compareAndSet(false, true)) {
            driveRpc(markFile, messaging, self)
        }

        // 玩家寻址落空：仅在定向 send 已成功送达后发一次（确保 approve 后才发，随机 UUID 名册无此人
        // → 控制面 failed(player_not_online) 落库），避免 approve 前被 403 丢弃而无落库行。
        if (msgReceived.get() && playerMissSent.compareAndSet(false, true)) {
            drivePlayerMiss(markFile, messaging)
        }
    }

    /** 注册收消息处理器（一次）：定向 on 记录收到并置停发；RPC on 收到请求即回信。 */
    private fun registerHandlers(
        markFile: File,
        messaging: top.wcpe.beacon.agent.api.Messaging,
    ) {
        if (!handlersRegistered.compareAndSet(false, true)) {
            return
        }
        messaging.on(TYPE_MSG) { incoming ->
            E2EObservation.append(
                markFile,
                "MSG_RECEIVED",
                incoming.type(),
                "-",
                "source=${incoming.source()};isRequest=${incoming.isRequest()};payload=${incoming.payload()}",
            )
            msgReceived.set(true)
        }
        messaging.on(TYPE_RPC) { incoming ->
            E2EObservation.append(
                markFile,
                "RPC_REQ_RECEIVED",
                incoming.type(),
                "-",
                "source=${incoming.source()};isRequest=${incoming.isRequest()};payload=${incoming.payload()}",
            )
            // RPC 请求：回信 = 前缀 + 请求 payload（响应亦是一条带 correlationId 的消息，经控制面回投本机长轮询）。
            if (incoming.isRequest()) {
                incoming.reply(RPC_REPLY_PREFIX + incoming.payload())
            }
        }
        // 广播订阅：本机 publish 的广播经控制面 fan-out 含自身，回投本机长轮询后按 topic 路由到此（与 on 分发隔离，FR-180）。
        messaging.subscribe(TYPE_BCAST) { topic, payload ->
            E2EObservation.append(markFile, "BCAST_RECEIVED", topic, "-", "payload=$payload")
            bcastReceived.set(true)
        }
        info("Beacon E2E 消息探针已注册 on($TYPE_MSG) / on($TYPE_RPC) / subscribe($TYPE_BCAST) 处理器")
    }

    /** 发一次 RPC call 并挂 whenComplete：完成记 RPC_REPLY 并置 rpcDone；异常记 RPC_ERROR；无论成败释放单飞。 */
    private fun driveRpc(
        markFile: File,
        messaging: top.wcpe.beacon.agent.api.Messaging,
        self: String,
    ) {
        try {
            messaging.call(self, TYPE_RPC, RPC_REQUEST).whenComplete { result, error ->
                if (error == null) {
                    E2EObservation.append(markFile, "RPC_REPLY", TYPE_RPC, "-", "payload=$result")
                    rpcDone.set(true)
                } else {
                    // approve 前请求被丢弃、Future 超时属预期：记录供观测，下一轮再试。
                    E2EObservation.append(markFile, "RPC_ERROR", TYPE_RPC, "-", "err=${error.message}")
                }
                rpcInFlight.set(false)
            }
        } catch (t: Throwable) {
            E2EObservation.append(markFile, "RPC_ERROR", TYPE_RPC, "-", "err=${t.message}")
            rpcInFlight.set(false)
        }
    }

    /** 玩家寻址落空：sendToPlayer 一个名册无此人的随机 UUID，控制面即记 failed(player_not_online)。 */
    private fun drivePlayerMiss(
        markFile: File,
        messaging: top.wcpe.beacon.agent.api.Messaging,
    ) {
        try {
            val accepted = messaging.sendToPlayer(MISSING_PLAYER_UUID, TYPE_MISS, MSG_PAYLOAD)
            E2EObservation.append(markFile, "PLAYER_MISS_SENT", TYPE_MISS, "-", "player=$MISSING_PLAYER_UUID;localAccepted=$accepted")
        } catch (t: Throwable) {
            E2EObservation.append(markFile, "PLAYER_MISS_ERROR", TYPE_MISS, "-", "err=${t.message}")
        }
    }
}
