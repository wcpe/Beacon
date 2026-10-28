package top.wcpe.beacon.agent.bungee.support

import net.md_5.bungee.api.chat.BaseComponent
import net.md_5.bungee.api.config.ServerInfo
import net.md_5.bungee.api.connection.Connection
import net.md_5.bungee.api.connection.Server
import java.net.InetSocketAddress
import java.net.SocketAddress

/**
 * [Server]（玩家当前挂载的后端子服连接）的测试替身（手写假实现，不用 mock 框架、不用反射）。
 *
 * 这是测试替身，只实现了被测代码真正用到的成员 [getInfo]——监听器用它把「玩家掉线前所在服」
 * 翻译成 serverId，交给选路层做区感知。其余成员（发包、断开、连接状态）与本能力无关，
 * 一律抛 [UnsupportedOperationException]：真被用到时立刻暴露，而不是静默返回伪造结果。
 */
class FakeServer(private val info: ServerInfo) : Server {
    override fun getInfo(): ServerInfo = info

    override fun sendData(
        channel: String,
        data: ByteArray,
    ): Unit = unsupported("sendData")

    override fun getAddress(): InetSocketAddress = unsupported("getAddress")

    override fun getSocketAddress(): SocketAddress = unsupported("getSocketAddress")

    override fun disconnect(reason: String): Unit = unsupported("disconnect")

    override fun disconnect(vararg messages: BaseComponent): Unit = unsupported("disconnect")

    override fun disconnect(reason: BaseComponent): Unit = unsupported("disconnect")

    override fun isConnected(): Boolean = unsupported("isConnected")

    override fun unsafe(): Connection.Unsafe = unsupported("unsafe")

    private fun unsupported(member: String): Nothing = throw UnsupportedOperationException("测试替身 FakeServer 未实现 $member")
}
