package top.wcpe.beacon.agent.bungee.support

import net.md_5.bungee.api.Callback
import net.md_5.bungee.api.ChatMessageType
import net.md_5.bungee.api.ServerConnectRequest
import net.md_5.bungee.api.ServerLink
import net.md_5.bungee.api.SkinConfiguration
import net.md_5.bungee.api.Title
import net.md_5.bungee.api.chat.BaseComponent
import net.md_5.bungee.api.config.ServerInfo
import net.md_5.bungee.api.connection.Connection
import net.md_5.bungee.api.connection.PendingConnection
import net.md_5.bungee.api.connection.ProxiedPlayer
import net.md_5.bungee.api.connection.Server
import net.md_5.bungee.api.dialog.Dialog
import net.md_5.bungee.api.event.ServerConnectEvent
import net.md_5.bungee.api.score.Scoreboard
import java.net.InetSocketAddress
import java.net.SocketAddress
import java.util.Locale
import java.util.UUID
import java.util.concurrent.CompletableFuture

/**
 * [ProxiedPlayer] 的测试替身（手写假实现，不用 mock 框架、不用反射）。
 *
 * 这是测试替身，只实现了被测代码真正用到的成员：
 * - [getName]：回退路由的日志文案要打印玩家名；
 * - [getServer]：崩溃路径用它推断「原所在服」，从而决定是否走同区大厅这一级。
 *
 * 其余成员（发包、聊天、切服、标签页、Cookie、传送等约 50 个）与被测能力无关，
 * 一律抛 [UnsupportedOperationException]：真被用到时立刻失败并指出方法名，
 * 避免"默认值假通过"——例如把 [connect] 做成空实现，就无法察觉监听器误改了玩家去向。
 */
class FakePlayer(
    private val name: String,
    private val server: Server?,
) : ProxiedPlayer {
    override fun getName(): String = name

    /** 返回构造时挂载的子服；传 `null` 表示玩家此刻已无归属（掉线服已摘除）。 */
    override fun getServer(): Server? = server

    override fun getDisplayName(): String = unsupported("getDisplayName")

    override fun setDisplayName(displayName: String): Unit = unsupported("setDisplayName")

    override fun sendMessage(
        type: ChatMessageType,
        vararg messages: BaseComponent,
    ): Unit = unsupported("sendMessage")

    override fun sendMessage(
        type: ChatMessageType,
        message: BaseComponent,
    ): Unit = unsupported("sendMessage")

    override fun sendMessage(
        sender: UUID,
        vararg messages: BaseComponent,
    ): Unit = unsupported("sendMessage")

    override fun sendMessage(
        sender: UUID,
        message: BaseComponent,
    ): Unit = unsupported("sendMessage")

    override fun connect(target: ServerInfo): Unit = unsupported("connect")

    override fun connect(
        target: ServerInfo,
        reason: ServerConnectEvent.Reason,
    ): Unit = unsupported("connect")

    override fun connect(
        target: ServerInfo,
        callback: Callback<Boolean>,
    ): Unit = unsupported("connect")

    override fun connect(
        target: ServerInfo,
        callback: Callback<Boolean>,
        reason: ServerConnectEvent.Reason,
    ): Unit = unsupported("connect")

    override fun connect(request: ServerConnectRequest): Unit = unsupported("connect")

    override fun getPing(): Int = unsupported("getPing")

    override fun sendData(
        channel: String,
        data: ByteArray,
    ): Unit = unsupported("sendData")

    override fun getPendingConnection(): PendingConnection = unsupported("getPendingConnection")

    override fun chat(message: String): Unit = unsupported("chat")

    override fun getReconnectServer(): ServerInfo = unsupported("getReconnectServer")

    override fun setReconnectServer(server: ServerInfo): Unit = unsupported("setReconnectServer")

    override fun getUUID(): String = unsupported("getUUID")

    override fun getUniqueId(): UUID = unsupported("getUniqueId")

    override fun getLocale(): Locale = unsupported("getLocale")

    override fun getViewDistance(): Byte = unsupported("getViewDistance")

    override fun getChatMode(): ProxiedPlayer.ChatMode = unsupported("getChatMode")

    override fun hasChatColors(): Boolean = unsupported("hasChatColors")

    override fun getSkinParts(): SkinConfiguration = unsupported("getSkinParts")

    override fun getMainHand(): ProxiedPlayer.MainHand = unsupported("getMainHand")

    override fun setTabHeader(
        header: BaseComponent,
        footer: BaseComponent,
    ): Unit = unsupported("setTabHeader")

    override fun setTabHeader(
        header: Array<BaseComponent>,
        footer: Array<BaseComponent>,
    ): Unit = unsupported("setTabHeader")

    override fun resetTabHeader(): Unit = unsupported("resetTabHeader")

    override fun sendTitle(title: Title): Unit = unsupported("sendTitle")

    override fun isForgeUser(): Boolean = unsupported("isForgeUser")

    override fun getModList(): Map<String, String> = unsupported("getModList")

    override fun getScoreboard(): Scoreboard = unsupported("getScoreboard")

    override fun retrieveCookie(key: String): CompletableFuture<ByteArray> = unsupported("retrieveCookie")

    override fun storeCookie(
        key: String,
        payload: ByteArray,
    ): Unit = unsupported("storeCookie")

    override fun transfer(
        host: String,
        port: Int,
    ): Unit = unsupported("transfer")

    override fun getClientBrand(): String = unsupported("getClientBrand")

    override fun clearDialog(): Unit = unsupported("clearDialog")

    override fun showDialog(dialog: Dialog): Unit = unsupported("showDialog")

    override fun sendServerLinks(links: List<ServerLink>): Unit = unsupported("sendServerLinks")

    // ---------- Connection ----------

    override fun getAddress(): InetSocketAddress = unsupported("getAddress")

    override fun getSocketAddress(): SocketAddress = unsupported("getSocketAddress")

    override fun disconnect(reason: String): Unit = unsupported("disconnect")

    override fun disconnect(vararg messages: BaseComponent): Unit = unsupported("disconnect")

    override fun disconnect(reason: BaseComponent): Unit = unsupported("disconnect")

    override fun isConnected(): Boolean = unsupported("isConnected")

    override fun unsafe(): Connection.Unsafe = unsupported("unsafe")

    // ---------- CommandSender ----------

    override fun sendMessage(message: String): Unit = unsupported("sendMessage")

    override fun sendMessages(vararg messages: String): Unit = unsupported("sendMessages")

    override fun sendMessage(vararg messages: BaseComponent): Unit = unsupported("sendMessage")

    override fun sendMessage(message: BaseComponent): Unit = unsupported("sendMessage")

    override fun getGroups(): Collection<String> = unsupported("getGroups")

    override fun addGroups(vararg groups: String): Unit = unsupported("addGroups")

    override fun removeGroups(vararg groups: String): Unit = unsupported("removeGroups")

    override fun hasPermission(permission: String): Boolean = unsupported("hasPermission")

    override fun setPermission(
        permission: String,
        value: Boolean,
    ): Unit = unsupported("setPermission")

    override fun getPermissions(): Collection<String> = unsupported("getPermissions")

    private fun unsupported(member: String): Nothing = throw UnsupportedOperationException("测试替身 FakePlayer 未实现 $member")
}
