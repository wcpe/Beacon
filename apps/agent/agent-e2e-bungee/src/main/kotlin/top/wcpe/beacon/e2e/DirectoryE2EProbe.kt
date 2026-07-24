package top.wcpe.beacon.e2e

import net.md_5.bungee.api.ProxyServer
import taboolib.common.platform.function.getDataFolder
import taboolib.common.platform.function.info
import taboolib.common.platform.function.submit
import java.io.File
import java.nio.file.AtomicMoveNotSupportedException
import java.nio.file.Files
import java.nio.file.StandardCopyOption

/**
 * 子服目录注入端到端验收探针（BungeeCord 壳）。
 *
 * 周期快照当前 Bungee ServerInfo 目录与 beacon 命令注册状态，覆写到数据目录下
 * e2e-directory-latest.txt（每轮覆写、只反映「当前」状态），供外部 Go 驱动断言：
 *  - ① BeaconAgentProxy 是否把在线 role=bukkit 子服按 serverId 注入目录（出现 `SERVER <serverId> ...`）；
 *  - ② mc-testkit 固定手工路由是否被保留（Beacon 只管自己创建的条目、不动手工配置）；
 *  - ③ 运行时代理实现是否为原生 BungeeCord（IMPLEMENTATION=BungeeCord）；
 *  - ④ beacon 主命令是否已在代理注册（COMMAND_BEACON=true）。
 *
 * 只读快照目录与命令注册状态，不发起任何传送动作。
 */
object DirectoryE2EProbe {
    /** 快照文件名：外部驱动据此断言当前目录状态。 */
    private const val SNAPSHOT_FILE = "e2e-directory-latest.txt"

    /** 轮询周期（tick，20 tick/秒）：约每 2 秒快照一次。 */
    private const val POLL_INTERVAL_TICKS = 40L

    /**
     * 由主插件 BeaconE2EBungee 在 ENABLE 时显式启动。
     *
     * 本探针不再各自作为 TabooLib Plugin：一个 TabooLib 插件只能有一个 Plugin 实例，
     * 多一个会在 PlatformFactory 注入时抛 IllegalStateException「Plugin instance already set」、
     * 整个插件加载失败。与 Bukkit 侧 OverrideE2EProbe / FileTreeE2EProbe 同款——普通 object + 主插件 start()。
     */
    fun start() {
        info("Beacon E2E（代理）目录探针已启用，快照文件=${File(getDataFolder(), SNAPSHOT_FILE).absolutePath}")
        startSnapshot()
    }

    /** 周期快照 ServerInfo 目录与命令注册状态，覆写快照文件（异步、不上代理主线程）。 */
    private fun startSnapshot() {
        submit(async = true, delay = POLL_INTERVAL_TICKS, period = POLL_INTERVAL_TICKS) {
            val proxy = ProxyServer.getInstance() ?: return@submit
            val sb = StringBuilder()
            sb.append("IMPLEMENTATION=").append(proxy.name).append('\n')
            sb.append("COMMAND_BEACON=").append(beaconCommandRegistered(proxy)).append('\n')
            // ServerInfo 目录快照：每个条目一行「SERVER <名称> <socketAddress>」。
            for ((name, server) in proxy.servers) {
                sb.append("SERVER ").append(name).append(' ').append(server.socketAddress.toString()).append('\n')
            }
            writeSnapshot(sb.toString())
        }
    }

    /** 探测 beacon 主命令是否已注册（API 形态差异时降级为 false，不抛异常打断快照）。 */
    private fun beaconCommandRegistered(proxy: ProxyServer): Boolean {
        return try {
            proxy.pluginManager.commands.any { it.key.equals("beacon", ignoreCase = true) }
        } catch (t: Throwable) {
            false
        }
    }

    /** 先写同目录临时文件再原子替换，避免外部 Go 驱动读到空文件或半份快照。 */
    @Synchronized
    private fun writeSnapshot(content: String) {
        val target = File(getDataFolder(), SNAPSHOT_FILE).toPath()
        Files.createDirectories(target.parent)
        val temp = Files.createTempFile(target.parent, ".$SNAPSHOT_FILE.", ".tmp")
        try {
            Files.write(temp, content.toByteArray(Charsets.UTF_8))
            try {
                Files.move(temp, target, StandardCopyOption.ATOMIC_MOVE, StandardCopyOption.REPLACE_EXISTING)
            } catch (_: AtomicMoveNotSupportedException) {
                Files.move(temp, target, StandardCopyOption.REPLACE_EXISTING)
            }
        } finally {
            Files.deleteIfExists(temp)
        }
    }
}
