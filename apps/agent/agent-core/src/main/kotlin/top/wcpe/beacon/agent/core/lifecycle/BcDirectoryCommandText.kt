package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.api.ServiceInstance
import top.wcpe.beacon.agent.core.proxy.ManagedDirectorySnapshot
import top.wcpe.beacon.agent.core.proxy.ManagedHealthFactState
import top.wcpe.beacon.agent.core.proxy.ManagedServerHealth
import java.time.Instant
import java.time.format.DateTimeFormatter

/** BC 受管目录本地查询的纯文本渲染；只消费一次已捕获的不可变快照。 */
object BcDirectoryCommandText {
    private const val PAGE_SIZE = 10
    private const val FIELD_LIMIT = 80

    /** BC 在共享命令后追加的帮助项；Bukkit 不调用本对象，既有帮助保持不变。 */
    val USAGE_LINES: List<String> =
        OpsCommandText.USAGE_LINES +
            listOf(
                "  servers  查看 Beacon 受管服务器目录（可选页码，每页 10 条）",
                "  server   查看指定 Beacon 受管服务器详情",
            )

    val HELP_LINES: List<String> =
        OpsCommandText.HELP_LINES +
            listOf(
                "  servers [页码]  分页查看 Beacon 受管服务器目录",
                "  server <serverId>  精确查看一台 Beacon 受管服务器",
            )

    fun statusLines(snapshot: ManagedDirectorySnapshot): List<String> {
        val candidates = snapshot.lobby.candidates
        return listOf(
            "  受管目录=${if (snapshot.version > 0L) "已同步" else "未同步"}",
            "  最近成功同步=${syncedAt(snapshot)}",
            "  受管服务器=${snapshot.entries.size} 台",
            "  大厅候选=${candidates.count { it.schedulable }}/${candidates.size} 台可调度",
        )
    }

    fun serversLines(
        snapshot: ManagedDirectorySnapshot,
        rawPage: String?,
    ): List<String> {
        val page = rawPage?.toIntOrNull()
        if (rawPage != null && (page == null || page < 1)) {
            return listOf("页码必须是大于等于 1 的整数", "用法：/beacon servers [页码]")
        }
        if (!synced(snapshot)) return listOf("受管目录尚未完成首次同步")
        val sorted = sortedEntries(snapshot)
        if (sorted.isEmpty()) return listOf("Beacon 受管服务器：当前目录为空；最近同步=${syncedAt(snapshot)}")
        val actualPage = page ?: 1
        val totalPages = (sorted.size + PAGE_SIZE - 1) / PAGE_SIZE
        if (actualPage > totalPages) return listOf("页码超出范围，当前共 $totalPages 页", "用法：/beacon servers [页码]")
        val start = (actualPage - 1) * PAGE_SIZE
        return buildList {
            add("Beacon 受管服务器：第 $actualPage/$totalPages 页，共 ${sorted.size} 台，每页 10 台；最近同步=${syncedAt(snapshot)}")
            sorted.subList(start, minOf(start + PAGE_SIZE, sorted.size)).forEach { entry ->
                val health = snapshot.healthOf(entry.serverId())
                add(
                    "  ${field(entry.serverId())}｜归属=${ownership(entry, snapshot)}｜在线=${online(entry.status())}｜" +
                        "健康=${healthLevel(health)}｜可调度=${schedulable(health)}",
                )
            }
        }
    }

    fun serverLines(
        snapshot: ManagedDirectorySnapshot,
        rawServerId: String?,
    ): List<String> {
        if (!synced(snapshot)) return listOf("受管目录尚未完成首次同步，暂不可查询单服详情")
        val serverId = rawServerId?.trim().orEmpty()
        if (serverId.isEmpty()) return listOf("serverId 不能为空", "用法：/beacon server <serverId>")
        val entry = snapshot.byServerId[serverId] ?: return listOf("未找到 Beacon 受管服务器：${field(serverId)}")
        val health = snapshot.healthOf(serverId)
        return listOf(
            "Beacon 受管服务器详情：${field(entry.serverId())}",
            "  目录来源=Beacon",
            "  拓扑归属=${detailOwnership(entry, snapshot)}",
            "  在线状态=${online(entry.status())}",
            "  健康状态=${healthLevel(health)}",
            "  可调度=${schedulable(health)}",
            "  不可调度原因=${reasons(health)}",
            "  目录快照=${snapshot.version}",
        )
    }

    private fun synced(snapshot: ManagedDirectorySnapshot): Boolean = snapshot.version > 0L

    private fun sortedEntries(snapshot: ManagedDirectorySnapshot): List<ServiceInstance> {
        val lobbyIds = snapshot.lobbyMemberIds
        return snapshot.entries.sortedWith(
            compareBy<ServiceInstance> {
                when {
                    it.serverId() in lobbyIds -> 0
                    it.group().isNotBlank() && it.zone().isNotBlank() -> 1
                    else -> 2
                }
            }.thenBy { if (it.serverId() in lobbyIds) field(it.serverId()) else field(it.group()) }
                .thenBy { if (it.serverId() in lobbyIds) "" else field(it.zone()) }
                .thenBy { field(it.serverId()) },
        )
    }

    private fun ownership(
        entry: ServiceInstance,
        snapshot: ManagedDirectorySnapshot,
    ): String =
        when {
            entry.serverId() in snapshot.lobbyMemberIds -> "全局大厅"
            entry.group().isNotBlank() && entry.zone().isNotBlank() -> "${field(entry.group())}/${field(entry.zone())}"
            else -> "未分配"
        }

    private fun detailOwnership(
        entry: ServiceInstance,
        snapshot: ManagedDirectorySnapshot,
    ): String =
        when (ownership(entry, snapshot)) {
            "全局大厅" -> "全局大厅"
            "未分配" -> "未分配"
            else -> "大区:${field(entry.group())}，小区:${field(entry.zone())}"
        }

    private fun online(raw: String): String =
        when (raw) {
            "online" -> "在线"
            "degraded" -> "降级"
            "offline" -> "离线"
            else -> field(raw)
        }

    private fun healthLevel(health: ManagedServerHealth): String =
        if (health.state == ManagedHealthFactState.NOT_REPORTED) "未上报" else field(health.level ?: "未知")

    private fun schedulable(health: ManagedServerHealth): String =
        when (health.state) {
            ManagedHealthFactState.NOT_REPORTED -> "未上报"
            ManagedHealthFactState.REPORTED -> if (health.schedulable == true) "是" else "否"
        }

    private fun reasons(health: ManagedServerHealth): String =
        when (health.state) {
            ManagedHealthFactState.NOT_REPORTED -> "未上报健康事实"
            ManagedHealthFactState.REPORTED ->
                if (health.schedulable == true) {
                    "无"
                } else if (health.reasons.isEmpty()) {
                    "未进入可调度候选"
                } else {
                    health.reasons.joinToString("、") { reason(it) }
                }
        }

    private fun reason(code: String): String =
        when (code) {
            "offline" -> "服务离线"
            "degraded" -> "服务降级"
            "capacity_full" -> "容量已满"
            "not_ready" -> "大厅未就绪"
            "no_candidate" -> "无可调度候选"
            else -> "未知原因（${field(code)}）"
        }

    private fun syncedAt(snapshot: ManagedDirectorySnapshot): String =
        snapshot.lastSuccessAtMs?.let { DateTimeFormatter.ISO_INSTANT.format(Instant.ofEpochMilli(it)) } ?: "尚未成功同步"

    private fun field(raw: String): String {
        val clean = raw.filterNot { it.isISOControl() || it == '\n' || it == '\r' }
        return if (clean.length <= FIELD_LIMIT) clean else clean.take(FIELD_LIMIT - 1) + "…"
    }
}
