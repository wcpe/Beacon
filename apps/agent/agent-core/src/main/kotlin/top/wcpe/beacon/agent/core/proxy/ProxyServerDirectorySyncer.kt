package top.wcpe.beacon.agent.core.proxy

import top.wcpe.beacon.agent.api.ServiceInstance
import top.wcpe.beacon.agent.core.client.DiscoveryFetchResult
import top.wcpe.beacon.agent.core.log.LogRedactor

/**
 * 同步 Beacon discovery 的 Bukkit 子服到 Proxy 服务器目录，并据小区默认入口设 BungeeCord 默认/fallback 服（FR-48）。
 *
 * @param directory  代理服务器目录（注入 / 移除子服 + 设默认服）
 * @param homeGroup  本代理服务的大区（空串=未配，则不设默认服 + 告警，FR-48）
 * @param homeZone   本代理服务的小区（空串=未配）
 * @param warn       WARN 日志（默认入口未配 / 无命中 / 不在线时告警，FR-48）
 * @param info       INFO 日志（注入子服 + 设默认服可观测，FR-48）
 * @param discover   拉取本 namespace 的发现结果；失败与权威空快照必须显式区分
 */
class ProxyServerDirectorySyncer(
    private val directory: ProxyServerDirectory,
    private val homeGroup: String = "",
    private val homeZone: String = "",
    private val warn: (String) -> Unit = {},
    private val info: (String) -> Unit = {},
    private val discover: () -> DiscoveryFetchResult<ServiceInstance>,
) {
    private val seenManaged: MutableSet<String> = linkedSetOf()

    // 上一轮已设的默认服 serverId，去重避免每轮重复打 INFO 日志 / 重复写 priority。
    private var lastDefaultServer: String? = null

    // 是否已为「选不出默认入口」打过 WARN，去重避免每轮（默认 10s）刷屏；选出默认服后复位。
    private var warnedNoDefault: Boolean = false

    fun syncOnce() {
        when (val result = discover()) {
            is DiscoveryFetchResult.Success -> syncSuccessfulSnapshot(result.instances)
            is DiscoveryFetchResult.Failed -> warn(LogRedactor.redact("发现 Beacon 子服失败，保留现有代理目录：${result.reason}"))
        }
    }

    private fun syncSuccessfulSnapshot(discovered: List<ServiceInstance>) {
        val instances = discovered.filter { it.role() == ROLE_BUKKIT && it.status() == STATUS_ONLINE }
        val desired = instances.map { it.serverId() }.toSet()
        for (instance in instances) {
            // BungeeCord 启动必须在 config.yml 写至少一个 servers 条目（常与大厅同名）。
            // 同名手工服不再跳过：接管为受管并同步 discovery 地址；upsert 仅在有变更时返回 true。
            val existedManual =
                directory.hasServer(instance.serverId()) && !directory.isManaged(instance.serverId())
            if (directory.upsertManaged(instance)) {
                seenManaged.add(instance.serverId())
                if (existedManual) {
                    info(
                        "接管 Proxy 同名手工服务器并由 Beacon 管理：${instance.serverId()} -> ${instance.address()}",
                    )
                } else {
                    info("注入 Beacon 子服到代理目录：${instance.serverId()} -> ${instance.address()}")
                }
            } else if (directory.isManaged(instance.serverId())) {
                // 已受管且无变更：仍记入本轮 desired，避免被 removeStale 误删
                seenManaged.add(instance.serverId())
            }
        }
        removeStale(desired)
        applyDefaultServer(discovered)
    }

    private fun removeStale(desired: Set<String>) {
        val stale = seenManaged.filter { it !in desired }.toList()
        for (serverId in stale) {
            directory.removeManaged(serverId)
            seenManaged.remove(serverId)
        }
    }

    /**
     * 只据 home-zone 在 Beacon 显式配置的默认入口设默认服（FR-48）。
     * 选不出（未配 home-zone / 该 zone 未设默认入口 / 默认入口当前不在线）→ **不设任何默认服** + 打一条 WARN
     * （去重，不每轮刷屏），玩家此时遇 BungeeCord 原生「无默认服」——这是「没配」的明确信号，绝不被静默落到非大厅服。
     * 选出的默认服与上轮相同则跳过（去重，不重复设 / 不刷屏）。
     */
    private fun applyDefaultServer(discovered: List<ServiceInstance>) {
        val target = DefaultEntrySelector.select(discovered, homeGroup, homeZone)
        if (target == null) {
            if (!warnedNoDefault) {
                val zoneCtx =
                    if (homeGroup.isBlank() || homeZone.isBlank()) {
                        "本代理未配 proxy.home-group / proxy.home-zone"
                    } else {
                        "home-zone=$homeGroup/$homeZone 的默认入口未在 Beacon 配置或当前不在线"
                    }
                warn("未设 BungeeCord 默认/fallback 服：$zoneCtx，请在 Beacon 为该小区配置默认入口（否则玩家加入将报无默认服）")
                warnedNoDefault = true
            }
            return
        }
        // 选出了默认入口：复位告警去重位，下次再选不出时重新告警。
        warnedNoDefault = false
        if (target == lastDefaultServer) {
            return
        }
        directory.setDefaultServer(target)
        lastDefaultServer = target
        info("设置 BungeeCord 默认/fallback 服为 $target（home-zone=$homeGroup/$homeZone）")
    }

    private companion object {
        const val ROLE_BUKKIT = "bukkit"
        const val STATUS_ONLINE = "online"
    }
}
