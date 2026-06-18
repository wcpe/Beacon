package top.wcpe.beacon.agent.core.testutil

import top.wcpe.beacon.agent.core.transport.StreamEvent
import top.wcpe.beacon.agent.core.transport.StreamListener
import top.wcpe.beacon.agent.core.transport.StreamRequest
import top.wcpe.beacon.agent.core.transport.StreamTransport
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicReference

/**
 * 测试用假 SSE 流：open 时记录上报的 URL（供断言连接即对账携带 md5），onOpen 后阻塞读取脚本指令队列，
 * 把脚本里的事件投递给 listener，遇 Close 指令则 onClosed 返回（模拟服务端关闭 / 断线）。
 *
 * 测试通过 [pushEvent] / [closeStream] 向当前在飞连接喂指令，断言生命周期对事件的反应。
 */
class FakeStreamTransport : StreamTransport {

    /** 累计 open 次数（断言重连发生）。 */
    val openCalls = AtomicInteger(0)

    /** 最近一次 open 的请求（断言 URL 携带各通道 md5）。 */
    val lastRequest = AtomicReference<StreamRequest?>(null)

    // 当前在飞连接的指令队列（每次 open 新建一条）。
    private val commands = AtomicReference<LinkedBlockingQueue<Command>?>(null)

    private sealed class Command {
        data class Emit(val event: StreamEvent) : Command()
        data class Close(val error: Throwable?) : Command()
    }

    override fun open(request: StreamRequest, listener: StreamListener) {
        openCalls.incrementAndGet()
        lastRequest.set(request)
        val queue = LinkedBlockingQueue<Command>()
        commands.set(queue)
        listener.onOpen()
        // 阻塞读取脚本指令（与真实 SSE 读流一致：同步阻塞直到关闭）。
        while (true) {
            when (val cmd = queue.take()) {
                is Command.Emit -> listener.onEvent(cmd.event)
                is Command.Close -> {
                    listener.onClosed(cmd.error)
                    return
                }
            }
        }
    }

    /** 向当前在飞连接投递一条事件。 */
    fun pushEvent(type: String, data: String = "") {
        commands.get()?.put(Command.Emit(StreamEvent(type = type, data = data)))
    }

    /** 关闭当前在飞连接（error=null 模拟正常关闭）。 */
    fun closeStream(error: Throwable? = null) {
        commands.get()?.put(Command.Close(error))
    }
}
