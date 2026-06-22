package top.wcpe.beacon.agent.core.proxy

import top.wcpe.beacon.agent.api.ServiceInstance

/**
 * 同步 Beacon discovery 的 Bukkit 子服到 Proxy 服务器目录，并据小区默认入口设 BungeeCord 默认/fallback 服（FR-48）。
 *
 * @param directory  代理服务器目录（注入 / 移除子服 + 设默认服）
 * @param homeGroup  本代理服务的大区（空串=未配，默认服走兜底，FR-48）
 * @param homeZone   本代理服务的小区（空串=未配）
 * @param warn       WARN 日志
 * @param info       INFO 日志（注入子服 + 设默认服可观测，FR-48）
 * @param discover   拉取本 namespace 的发现结果（在线 bukkit + 默认入口标志）
 */
class ProxyServerDirectorySyncer(
    private val directory: ProxyServerDirectory,
    private val homeGroup: String = "",
    private val homeZone: String = "",
    private val warn: (String) -> Unit = {},
    private val info: (String) -> Unit = {},
    private val discover: () -> List<ServiceInstance>,
) {
    private val seenManaged: MutableSet<String> = linkedSetOf()
    // 上一轮已设的默认服 serverId，去重避免每轮重复打 INFO 日志 / 重复写 priority。
    private var lastDefaultServer: String? = null

    fun syncOnce() {
        val discovered = discover()
        val instances = discovered.filter { it.role() == ROLE_BUKKIT && it.status() == STATUS_ONLINE }
        val desired = instances.map { it.serverId() }.toSet()
        for (instance in instances) {
            if (directory.hasServer(instance.serverId()) && !directory.isManaged(instance.serverId())) {
                warn("跳过 Beacon 子服注入：Proxy 已存在同名手工服务器 ${instance.serverId()}")
                continue
            }
            if (directory.upsertManaged(instance)) {
                seenManaged.add(instance.serverId())
                info("注入 Beacon 子服到代理目录：${instance.serverId()} -> ${instance.address()}")
            }
        }
        val stale = seenManaged.filter { it !in desired }.toList()
        for (serverId in stale) {
            directory.removeManaged(serverId)
            seenManaged.remove(serverId)
        }
        applyDefaultServer(discovered)
    }

    /**
     * 据小区默认入口（命中 home-zone）或兜底（首个在线 bukkit）选默认服并设进代理（FR-48）。
     * 选不出（无在线 bukkit）则不动作，等下一轮；选出的默认服与上轮相同则跳过（去重，不重复设 / 不刷屏）。
     */
    private fun applyDefaultServer(discovered: List<ServiceInstance>) {
        val target = DefaultEntrySelector.select(discovered, homeGroup, homeZone)
        if (target == null) {
            // 无可用在线 bukkit：保留上轮默认服不动（避免误清），等下一轮有服再设。
            return
        }
        if (target == lastDefaultServer) {
            return
        }
        directory.setDefaultServer(target)
        lastDefaultServer = target
        val via = if (homeGroup.isNotBlank() && homeZone.isNotBlank()) "home-zone=$homeGroup/$homeZone" else "兜底首个在线子服"
        info("设置 BungeeCord 默认/fallback 服为 $target（$via）")
    }

    private companion object {
        const val ROLE_BUKKIT = "bukkit"
        const val STATUS_ONLINE = "online"
    }
}
