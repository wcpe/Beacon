package top.wcpe.beacon.agent.core.lifecycle

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue
import top.wcpe.beacon.agent.api.ServiceInstance
import top.wcpe.beacon.agent.core.client.CandidateEntry
import top.wcpe.beacon.agent.core.proxy.ManagedDirectorySnapshot
import top.wcpe.beacon.agent.core.proxy.ManagedServerHealth
import top.wcpe.beacon.agent.core.proxy.ManagedLobbySnapshot

class BcDirectoryCommandTextTest {
    @Test
    fun `列表固定十条并按大厅 大区小区 未分配稳定排序`() {
        val entries =
            listOf(
                server("unassigned"),
                server("zone-b", group = "华东", zone = "二区"),
                server("lobby-z"),
                server("zone-a", group = "华东", zone = "一区"),
                server("lobby-a"),
            ) + (1..7).map { server("extra-$it", group = "华北", zone = "一区") }
        val snapshot = snapshot(entries, lobbyIds = listOf("lobby-z", "lobby-a"))

        val lines = BcDirectoryCommandText.serversLines(snapshot, "1")

        assertTrue(lines.first().contains("第 1/2 页，共 12 台，每页 10 台"))
        val ids = lines.drop(1).filter { it.startsWith("  ") }.map { it.substringAfter("  ").substringBefore("｜") }
        assertEquals(listOf("lobby-a", "lobby-z", "zone-a", "zone-b", "extra-1", "extra-2", "extra-3", "extra-4", "extra-5", "extra-6"), ids)
    }

    @Test
    fun `未同步 非法页 越界和未知服务均有确定中文输出`() {
        val unsynced = snapshot(emptyList(), version = 0)
        assertTrue(BcDirectoryCommandText.serversLines(unsynced, null).single().contains("尚未完成首次同步"))
        assertTrue(BcDirectoryCommandText.serverLines(unsynced, "x").single().contains("尚未完成首次同步"))

        val synced = snapshot(listOf(server("known")))
        assertTrue(BcDirectoryCommandText.serversLines(synced, "0").first().contains("页码必须"))
        assertTrue(BcDirectoryCommandText.serversLines(synced, "2").first().contains("页码超出范围"))
        assertTrue(BcDirectoryCommandText.serverLines(synced, "unknown").single().contains("未找到 Beacon 受管服务器"))
    }

    @Test
    fun `详情按快照索引查询并且状态摘要只统计大厅候选`() {
        val snapshot =
            snapshot(
                entries = listOf(server("lobby-1"), server("zone-1", group = "华东", zone = "一区")),
                lobbyIds = listOf("lobby-1"),
                lobbySchedulable = false,
            )

        val detail = BcDirectoryCommandText.serverLines(snapshot, "lobby-1").joinToString("\n")
        val status = BcDirectoryCommandText.statusLines(snapshot).joinToString("\n")

        assertTrue(detail.contains("目录来源=Beacon"))
        assertTrue(detail.contains("拓扑归属=全局大厅"))
        assertTrue(detail.contains("可调度=否"))
        assertTrue(detail.contains("不可调度原因=未进入可调度候选"))
        assertTrue(status.contains("受管服务器=2 台"))
        assertTrue(status.contains("大厅候选=0/1 台可调度"))
    }

    @Test
    fun `未上报健康事实与未知原因均明确展示且BC帮助不改共享帮助`() {
        val snapshot = snapshot(listOf(server("plain")))
        val detail = BcDirectoryCommandText.serverLines(snapshot, "plain").joinToString("\n")

        assertTrue(detail.contains("健康状态=未上报"))
        assertTrue(detail.contains("可调度=未上报"))
        assertTrue(detail.contains("不可调度原因=未上报健康事实"))
        assertTrue(BcDirectoryCommandText.HELP_LINES.any { it.contains("servers [页码]") })
        assertTrue(OpsCommandText.HELP_LINES.none { it.contains("servers [页码]") })
    }

    @Test
    fun `大厅成员归属来自独立成员集合而非可调度候选`() {
        val snapshot =
            snapshot(
                entries = listOf(server("zone-1", group = "华东", zone = "一区"), server("lobby-draining")),
                lobbyMemberIds = setOf("lobby-draining"),
            )

        val lines = BcDirectoryCommandText.serversLines(snapshot, null)
        val detail = BcDirectoryCommandText.serverLines(snapshot, "lobby-draining").joinToString("\n")

        assertTrue(lines[1].contains("lobby-draining｜归属=全局大厅"))
        assertTrue(detail.contains("拓扑归属=全局大厅"))
        assertTrue(detail.contains("健康状态=未上报"))
    }

    private fun snapshot(
        entries: List<ServiceInstance>,
        lobbyIds: List<String> = emptyList(),
        lobbyMemberIds: Set<String> = lobbyIds.toSet(),
        lobbySchedulable: Boolean = true,
        version: Long = 1,
    ): ManagedDirectorySnapshot {
        val candidates = lobbyIds.map { CandidateEntry(it, 90, "healthy", lobbySchedulable, 1, 100) }
        return ManagedDirectorySnapshot(
            version = version,
            lastSuccessAtMs = if (version == 0L) null else 0L,
            firstSync = version == 0L,
            entries = entries,
            byServerId = entries.associateBy { it.serverId() },
            lobbyMemberIds = lobbyMemberIds,
            healthByServerId =
                entries.associate { entry ->
                    val candidate = candidates.firstOrNull { it.serverId == entry.serverId() }
                    entry.serverId() to (candidate?.let(ManagedServerHealth::reported) ?: ManagedServerHealth.notReported())
                },
            lobby = ManagedLobbySnapshot(1, lobbySchedulable, candidates, null),
        )
    }

    private fun server(serverId: String, group: String = "", zone: String = ""): ServiceInstance =
        ServiceInstance(serverId, "bukkit", group, zone, "127.0.0.1:25565", "1.0.0", "online", 1, 100, 1)
}
