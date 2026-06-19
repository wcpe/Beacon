package top.wcpe.beacon.agent.core.api

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.messaging.RosterDirectory
import top.wcpe.beacon.agent.core.messaging.RosterDirectoryHolder
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.BackoffSettings
import top.wcpe.beacon.agent.core.settings.FileTreeSettings
import top.wcpe.beacon.agent.core.settings.OverrideSettings
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.util.concurrent.atomic.AtomicReference
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * DiscoveryView 名册只读查询单测（FR-31 / ADR-0022）：
 * - roster() 全表返回名册全部条目；
 * - rosterInZone(group, zone) 用控制面发现解出的 zone→serverId 集 ∩ 全表名册过滤，名册不臆造 zone；
 * - 名册不可用（holder 未注入实现 / 实现返回空）→ roster()/rosterInZone() 返空 Map、不抛、不崩。
 */
class DiscoveryViewRosterTest {

    private fun settings() = AgentSettings(
        endpoints = listOf("http://localhost:8848"),
        bootstrapToken = "tk",
        pollTimeoutMs = 50,
        requestTimeoutMs = 200,
        heartbeatFallbackMs = 100_000,
        backoff = BackoffSettings(initialMs = 50, maxMs = 50, multiplier = 1.0, jitterRatio = 0.0),
        snapshotEnabled = false,
        snapshotFileName = "snapshot.json",
        fileTree = FileTreeSettings(enabled = false, targetSubDir = "", appliedManifestFileName = "file-tree.applied.json"),
        override = OverrideSettings(commandWhitelist = emptySet(), backupDirName = "override-backup"),
    )

    /** 假名册：固定 map。 */
    private class FakeRosterDirectory(private val table: Map<String, String>) : RosterDirectory {
        override fun snapshot(): Map<String, String> = table
    }

    /**
     * 假 codec：把 instances 的 serverId 列表编进固定树，供 instancesInZone 解出 zone 的 serverId 集。
     *
     * decode 直接返回构造期注入的 instances 树（忽略入参 json），脚本化「控制面发现结果」。
     */
    private class InstancesCodec(private val serverIds: List<String>) : JsonCodec {
        override fun encode(value: Any?): String = "{}"
        override fun decode(json: String): Any? {
            val instances = serverIds.map { sid -> mapOf<String, Any?>("serverId" to sid, "zone" to "z") }
            return mapOf("instances" to instances)
        }
    }

    /** 占位 transport：回固定 200，不关心 URL。 */
    private class StubTransport : HttpTransport {
        val lastUrl = AtomicReference<String?>(null)
        override fun execute(request: HttpRequest): HttpResponse {
            lastUrl.set(request.url)
            return HttpResponse(200, "{}")
        }
    }

    private fun apiClient(serverIds: List<String>): BeaconApiClient =
        BeaconApiClient(StubTransport(), InstancesCodec(serverIds), settings())

    @Test
    fun `roster 全表返回名册全部条目`() {
        val table = mapOf("Alice" to "lobby-1", "Bob" to "game-1", "Carol" to "game-2")
        val holder = RosterDirectoryHolder().apply { set(FakeRosterDirectory(table)) }
        val view = DiscoveryView(apiClient(emptyList()), TopologyWatchHub(), holder)

        assertEquals(table, view.roster(), "roster() 应原样返回名册全表")
    }

    @Test
    fun `rosterInZone 仅返回 zone serverId 集内的玩家`() {
        // 名册：Alice/Bob 在该 zone 的两台子服，Carol 在 zone 外的 game-9。
        val table = mapOf("Alice" to "lobby-1", "Bob" to "game-1", "Carol" to "game-9")
        val holder = RosterDirectoryHolder().apply { set(FakeRosterDirectory(table)) }
        // 控制面发现：该 zone 的 serverId 集 = {lobby-1, game-1}（不含 game-9）。
        val view = DiscoveryView(apiClient(listOf("lobby-1", "game-1")), TopologyWatchHub(), holder)

        val filtered = view.rosterInZone("g1", "z1")
        assertEquals(
            mapOf("Alice" to "lobby-1", "Bob" to "game-1"),
            filtered,
            "rosterInZone 只应含 value 落在该 zone serverId 集内的玩家，zone 外玩家被排除",
        )
        assertTrue("Carol" !in filtered, "zone 外子服 game-9 上的 Carol 不应出现")
    }

    @Test
    fun `rosterInZone 该 zone 无人时返空 Map`() {
        val table = mapOf("Alice" to "lobby-1", "Bob" to "game-1")
        val holder = RosterDirectoryHolder().apply { set(FakeRosterDirectory(table)) }
        // 该 zone 的 serverId 集与名册无交集（zone 上的子服当前没人）。
        val view = DiscoveryView(apiClient(listOf("game-7", "game-8")), TopologyWatchHub(), holder)

        assertTrue(view.rosterInZone("g1", "z1").isEmpty(), "交集为空应返空 Map（非 null）")
    }

    @Test
    fun `名册未注入实现时 roster 与 rosterInZone 返空 Map 不抛`() {
        // holder 未 set 任何实现（messaging 模块未开 / Redis 未连）→ 优雅降级返空。
        val holder = RosterDirectoryHolder()
        val view = DiscoveryView(apiClient(listOf("lobby-1")), TopologyWatchHub(), holder)

        assertTrue(view.roster().isEmpty(), "名册不可用时 roster() 应返空 Map")
        assertTrue(view.rosterInZone("g1", "z1").isEmpty(), "名册不可用时 rosterInZone() 应返空 Map")
    }

    @Test
    fun `名册实现返空时 roster 与 rosterInZone 返空 Map`() {
        val holder = RosterDirectoryHolder().apply { set(FakeRosterDirectory(emptyMap())) }
        val view = DiscoveryView(apiClient(listOf("lobby-1")), TopologyWatchHub(), holder)

        assertTrue(view.roster().isEmpty(), "名册为空时 roster() 应返空 Map")
        assertTrue(view.rosterInZone("g1", "z1").isEmpty(), "名册为空时 rosterInZone() 应返空 Map")
    }
}
