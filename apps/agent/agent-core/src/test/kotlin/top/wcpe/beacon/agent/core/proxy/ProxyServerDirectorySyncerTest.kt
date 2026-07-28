package top.wcpe.beacon.agent.core.proxy

import top.wcpe.beacon.agent.api.ServiceInstance
import top.wcpe.beacon.agent.core.client.CandidateEntry
import top.wcpe.beacon.agent.core.client.DiscoveryFetchResult
import top.wcpe.beacon.agent.core.client.LobbyCandidates
import top.wcpe.beacon.agent.core.scheduling.CandidateSnapshot
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue

class ProxyServerDirectorySyncerTest {
    @Test
    fun `syncOnce 注入在线 Bukkit 子服`() {
        val directory = FakeDirectory()
        val syncer = ProxyServerDirectorySyncer(directory) { success(instance("lobby-1", "10.0.0.7:25565")) }

        syncer.syncOnce()

        assertTrue(directory.managed.contains("lobby-1"))
        assertEquals("10.0.0.7:25565", directory.addresses["lobby-1"])
    }

    @Test
    fun `syncOnce 接管同名手工服务器`() {
        val directory = FakeDirectory(manual = mutableSetOf("lobby-1"))
        val infos = mutableListOf<String>()
        val syncer =
            ProxyServerDirectorySyncer(directory, info = infos::add) {
                success(instance("lobby-1", "10.0.0.7:25565"))
            }

        syncer.syncOnce()

        assertTrue(directory.managed.contains("lobby-1"))
        assertEquals("10.0.0.7:25565", directory.addresses["lobby-1"])
        assertTrue(infos.any { it.contains("接管") && it.contains("lobby-1") })
    }

    @Test
    fun `syncOnce 成功空列表会移除已消失的受管服务器`() {
        var result: DiscoveryFetchResult<ServiceInstance> = success(instance("lobby-1", "10.0.0.7:25565"))
        val directory = FakeDirectory()
        val syncer = ProxyServerDirectorySyncer(directory) { result }
        syncer.syncOnce()

        result = success()
        syncer.syncOnce()

        assertTrue(!directory.managed.contains("lobby-1"))
        assertEquals(listOf("lobby-1"), directory.removed)
        assertTrue(syncer.snapshot().entries.isEmpty())
        assertTrue(syncer.snapshot().lobby.candidates.isEmpty())
    }

    @Test
    fun `syncOnce 权威空目录清除旧大厅候选`() {
        var result: DiscoveryFetchResult<ServiceInstance> = success(instance("lobby-1", "10.0.0.7:25565"))
        val lobby = LobbyCandidates(12L, true, listOf(CandidateEntry("lobby-1", 90, "healthy", true, 3, 100)))
        val syncer =
            ProxyServerDirectorySyncer(
                directory = FakeDirectory(),
                lobbySnapshot = { CandidateSnapshot(1L, 2L, emptyMap(), lobby) },
            ) { result }
        syncer.syncOnce()

        result = success()
        syncer.syncOnce()

        assertTrue(syncer.snapshot().entries.isEmpty())
        assertTrue(syncer.snapshot().lobby.candidates.isEmpty())
        assertEquals("no_candidate", syncer.snapshot().lobby.reason)
    }

    @Test
    fun `syncOnce 成功帧发布目录与大厅候选`() {
        val lobby =
            LobbyCandidates(
                12L,
                true,
                listOf(CandidateEntry("lobby-1", 90, "healthy", true, 3, 100)),
            )
        val syncer =
            ProxyServerDirectorySyncer(
                directory = FakeDirectory(),
                discover = { success(instance("lobby-1", "10.0.0.7:25565")) },
                lobbySnapshot = { CandidateSnapshot(1L, 2L, emptyMap(), lobby) },
                now = { 3L },
            )

        syncer.syncOnce()

        val published = syncer.snapshot()
        assertEquals(1L, published.version)
        assertEquals(3L, published.lastSuccessAtMs)
        assertTrue(published.firstSync)
        assertEquals(setOf("lobby-1"), published.byServerId.keys)
        assertEquals(lobby.candidates, published.lobby.candidates)
    }

    @Test
    fun `syncOnce 同帧发布每台受管服的健康事实或未上报状态`() {
        val lobby =
            LobbyCandidates(
                12L,
                true,
                listOf(CandidateEntry("lobby-1", 90, "healthy", true, 3, 100, listOf("capacity_ok"))),
            )
        val syncer =
            ProxyServerDirectorySyncer(
                directory = FakeDirectory(),
                discover =
                    {
                        success(
                            instance("lobby-1", "10.0.0.7:25565"),
                            instance("ordinary-1", "10.0.0.8:25565"),
                        )
                    },
                lobbySnapshot = { CandidateSnapshot(1L, 2L, emptyMap(), lobby) },
            )

        syncer.syncOnce()

        val reported = syncer.snapshot().healthByServerId.getValue("lobby-1")
        assertEquals(ManagedHealthFactState.REPORTED, reported.state)
        assertEquals("healthy", reported.level)
        assertEquals(true, reported.schedulable)
        assertEquals(90, reported.score)
        assertEquals(3, reported.onlineCount)
        assertEquals(100, reported.maxOnline)
        assertEquals(listOf("capacity_ok"), reported.reasons)
        assertEquals(ManagedHealthFactState.NOT_REPORTED, syncer.snapshot().healthByServerId.getValue("ordinary-1").state)
    }

    @Test
    fun `syncOnce 不可候选大厅成员仍保留权威大厅归属`() {
        val syncer =
            ProxyServerDirectorySyncer(
                directory = FakeDirectory(),
                discover = { success(instance("lobby-draining", "10.0.0.7:25565", lobbyMember = true)) },
                lobbySnapshot = { CandidateSnapshot(1L, 2L, emptyMap(), LobbyCandidates(12L, false, emptyList())) },
            )

        syncer.syncOnce()

        assertEquals(setOf("lobby-draining"), syncer.snapshot().lobbyMemberIds)
        assertTrue(syncer.snapshot().lobby.candidates.isEmpty())
        assertEquals(ManagedHealthFactState.NOT_REPORTED, syncer.snapshot().healthOf("lobby-draining").state)
    }

    @Test
    fun `syncOnce 发现失败会保留受管目录与已发布快照`() {
        var result: DiscoveryFetchResult<ServiceInstance> =
            success(instance("lobby-1", "10.0.0.7:25565", defaultEntry = true))
        val directory = FakeDirectory()
        val warnings = mutableListOf<String>()
        val syncer =
            ProxyServerDirectorySyncer(
                directory = directory,
                homeGroup = "area1",
                homeZone = "zoneA",
                warn = warnings::add,
            ) { result }
        syncer.syncOnce()
        val upsertCalls = directory.upsertCalls
        val published = syncer.snapshot()

        result = DiscoveryFetchResult.Failed("发现请求失败")
        syncer.syncOnce()

        assertEquals(setOf("lobby-1"), directory.managed)
        assertEquals(upsertCalls, directory.upsertCalls)
        assertTrue(directory.removed.isEmpty())
        assertEquals(0, directory.setDefaultCalls)
        assertEquals(published, syncer.snapshot())
        assertTrue(warnings.single().contains("发现请求失败"))
    }

    @Test
    fun `syncOnce 记录失败原因前会脱敏`() {
        val warnings = mutableListOf<String>()
        val syncer =
            ProxyServerDirectorySyncer(FakeDirectory(), warn = warnings::add) {
                DiscoveryFetchResult.Failed("token=plain-token Authorization: Bearer plain-authorization")
            }

        syncer.syncOnce()

        val warning = warnings.single()
        assertTrue(warning.contains("token=***"))
        assertTrue(warning.contains("Authorization: Bearer ***"))
        assertTrue(!warning.contains("plain-token"))
        assertTrue(!warning.contains("plain-authorization"))
    }

    @Test
    fun `syncOnce 同步在线和降级 bukkit 实例`() {
        val directory = FakeDirectory()
        val syncer =
            ProxyServerDirectorySyncer(directory) {
                success(
                    instance("lobby-1", "10.0.0.7:25565", role = "bukkit", status = "online"),
                    instance("lobby-2", "10.0.0.10:25565", role = "bukkit", status = "degraded"),
                    instance("proxy-2", "10.0.0.8:25577", role = "bungee", status = "online"),
                    instance("lost-1", "10.0.0.9:25565", role = "bukkit", status = "lost"),
                )
            }

        syncer.syncOnce()

        assertEquals(setOf("lobby-1", "lobby-2"), directory.managed)
    }

    @Test
    fun `syncOnce 不再据 home-zone 写默认优先级`() {
        val directory = FakeDirectory()
        val warnings = mutableListOf<String>()
        val syncer =
            ProxyServerDirectorySyncer(
                directory,
                homeGroup = "area1",
                homeZone = "zoneA",
                warn = warnings::add,
            ) {
                success(
                    instance("lobby-1", "10.0.0.7:25565", defaultEntry = false),
                    instance("lobby-2", "10.0.0.8:25565", defaultEntry = true),
                )
            }

        syncer.syncOnce()

        assertNull(directory.capturedDefault)
        assertEquals(0, directory.setDefaultCalls)
        assertTrue(warnings.isEmpty())
    }

    @Test
    fun `syncOnce 未配 home-zone 时不推断默认服`() {
        val directory = FakeDirectory()
        val warnings = mutableListOf<String>()
        val syncer =
            ProxyServerDirectorySyncer(directory, warn = warnings::add) {
                success(
                    instance("lobby-1", "10.0.0.7:25565", defaultEntry = false),
                    instance("lobby-2", "10.0.0.8:25565", defaultEntry = true),
                )
            }

        syncer.syncOnce()

        // 旧字段不参与新路径；由 Bungee 壳层按滚动升级策略提示一次。
        assertNull(directory.capturedDefault)
        assertEquals(0, directory.setDefaultCalls)
        assertTrue(warnings.isEmpty())
    }

    @Test
    fun `syncOnce 配了 home-zone 但该 zone 无默认入口也不写优先级`() {
        val directory = FakeDirectory()
        val warnings = mutableListOf<String>()
        val syncer =
            ProxyServerDirectorySyncer(
                directory,
                homeGroup = "area1",
                homeZone = "zoneA",
                warn = warnings::add,
            ) {
                // 新路径不读取小区默认入口标记。
                success(instance("lobby-1", "10.0.0.7:25565", defaultEntry = false))
            }

        syncer.syncOnce()

        assertNull(directory.capturedDefault)
        assertEquals(0, directory.setDefaultCalls)
        assertTrue(warnings.isEmpty())
    }

    @Test
    fun `syncOnce 默认入口离线时不写优先级`() {
        val directory = FakeDirectory()
        val warnings = mutableListOf<String>()
        val syncer =
            ProxyServerDirectorySyncer(
                directory,
                homeGroup = "area1",
                homeZone = "zoneA",
                warn = warnings::add,
            ) {
                success(instance("lobby-1", "10.0.0.7:25565", status = "lost", defaultEntry = true))
            }

        syncer.syncOnce()

        // 默认入口虽配但当前 lost；新路径不读取小区默认入口。
        assertNull(directory.capturedDefault)
        assertTrue(warnings.isEmpty())
    }

    @Test
    fun `syncOnce 不为旧默认入口链路写告警`() {
        val directory = FakeDirectory()
        val warnings = mutableListOf<String>()
        val syncer =
            ProxyServerDirectorySyncer(directory, warn = warnings::add) {
                success(instance("lobby-1", "10.0.0.7:25565", defaultEntry = false))
            }

        syncer.syncOnce()
        syncer.syncOnce()
        syncer.syncOnce()

        assertTrue(warnings.isEmpty())
    }

    @Test
    fun `syncOnce 不调用默认优先级接口`() {
        val directory = FakeDirectory()
        val syncer =
            ProxyServerDirectorySyncer(directory, homeGroup = "area1", homeZone = "zoneA") {
                success(instance("lobby-1", "10.0.0.7:25565", defaultEntry = true))
            }

        syncer.syncOnce()
        syncer.syncOnce()

        assertEquals(0, directory.setDefaultCalls)
        assertNull(directory.capturedDefault)
    }

    private fun success(vararg instances: ServiceInstance): DiscoveryFetchResult<ServiceInstance> =
        DiscoveryFetchResult.Success(instances.toList())

    private fun instance(
        serverId: String,
        address: String,
        role: String = "bukkit",
        status: String = "online",
        defaultEntry: Boolean = false,
        lobbyMember: Boolean = false,
    ): ServiceInstance {
        return ServiceInstance(serverId, role, "area1", "zoneA", address, "1.0", status, 0, 200, 100, defaultEntry, lobbyMember)
    }

    private class FakeDirectory(
        val manual: MutableSet<String> = mutableSetOf(),
    ) : ProxyServerDirectory {
        val managed: MutableSet<String> = mutableSetOf()
        val addresses: MutableMap<String, String> = mutableMapOf()
        val removed: MutableList<String> = mutableListOf()
        var upsertCalls: Int = 0
        var capturedDefault: String? = null
        var setDefaultCalls: Int = 0

        override fun hasServer(serverId: String): Boolean = manual.contains(serverId) || managed.contains(serverId)

        override fun isManaged(serverId: String): Boolean = managed.contains(serverId)

        override fun upsertManaged(instance: ServiceInstance): Boolean {
            upsertCalls++
            // 与 Bungee 实现一致：同名手工服可被接管
            manual.remove(instance.serverId())
            managed.add(instance.serverId())
            addresses[instance.serverId()] = instance.address()
            return true
        }

        override fun removeManaged(serverId: String) {
            removed.add(serverId)
            managed.remove(serverId)
            addresses.remove(serverId)
        }

        override fun setDefaultServer(serverId: String) {
            capturedDefault = serverId
            setDefaultCalls++
        }

        // 后端集合（FR-36）：本测桩取手工 + 受管并集，对齐真实代理「目录全部 keys」语义。
        override fun backendServerIds(): Set<String> = (manual + managed).toSet()
    }
}
