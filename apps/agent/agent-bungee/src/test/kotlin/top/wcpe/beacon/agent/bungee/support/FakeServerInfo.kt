package top.wcpe.beacon.agent.bungee.support

import net.md_5.bungee.api.Callback
import net.md_5.bungee.api.CommandSender
import net.md_5.bungee.api.ServerPing
import net.md_5.bungee.api.config.ServerInfo
import net.md_5.bungee.api.connection.ProxiedPlayer
import java.net.InetSocketAddress
import java.net.SocketAddress

/**
 * [ServerInfo] 的测试替身（手写假实现，不用 mock 框架、不用反射）。
 *
 * 这是测试替身，只实现了被测代码真正用到的成员 [getName]（回退路由用它拼日志、做目标比较）。
 * 其余成员一律抛 [UnsupportedOperationException]：一旦被测代码开始依赖它们，测试会立刻失败并
 * 指出具体方法，而不是被一个"看起来合理"的默认值悄悄掩盖（默认值会伪造出未被验证的语义）。
 */
class FakeServerInfo(private val name: String) : ServerInfo {
    override fun getName(): String = name

    override fun getAddress(): InetSocketAddress = unsupported("getAddress")

    override fun getSocketAddress(): SocketAddress = unsupported("getSocketAddress")

    override fun getPlayers(): Collection<ProxiedPlayer> = unsupported("getPlayers")

    override fun getMotd(): String = unsupported("getMotd")

    override fun isRestricted(): Boolean = unsupported("isRestricted")

    override fun getPermission(): String = unsupported("getPermission")

    override fun canAccess(sender: CommandSender): Boolean = unsupported("canAccess")

    override fun sendData(
        channel: String,
        data: ByteArray,
    ): Unit = unsupported("sendData")

    override fun sendData(
        channel: String,
        data: ByteArray,
        queue: Boolean,
    ): Boolean = unsupported("sendData")

    override fun ping(callback: Callback<ServerPing>): Unit = unsupported("ping")

    private fun unsupported(member: String): Nothing = throw UnsupportedOperationException("测试替身 FakeServerInfo 未实现 $member")
}
