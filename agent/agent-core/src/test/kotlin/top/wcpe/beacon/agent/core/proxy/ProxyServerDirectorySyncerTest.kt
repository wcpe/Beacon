package top.wcpe.beacon.agent.core.proxy

import top.wcpe.beacon.agent.api.ServiceInstance
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue

class ProxyServerDirectorySyncerTest {

    @Test
    fun `syncOnce 注入在线 Bukkit 子服`() {
        val directory = FakeDirectory()
        val syncer = ProxyServerDirectorySyncer(directory) { listOf(instance("lobby-1", "10.0.0.7:25565")) }

        syncer.syncOnce()

        assertTrue(directory.managed.contains("lobby-1"))
        assertEquals("10.0.0.7:25565", directory.addresses["lobby-1"])
    }

    @Test
    fun `syncOnce 跳过同名手工服务器`() {
        val directory = FakeDirectory(manual = mutableSetOf("lobby-1"))
        val warnings = mutableListOf<String>()
        val syncer = ProxyServerDirectorySyncer(directory, warn = warnings::add) {
            listOf(instance("lobby-1", "10.0.0.7:25565"))
        }

        syncer.syncOnce()

        assertTrue(!directory.managed.contains("lobby-1"))
        // 跳过同名手工服务器会告警；本测未配 home-zone 故另有一条「不设默认服」WARN，这里只校验前者。
        assertTrue(warnings.any { it.contains("lobby-1") })
    }

    @Test
    fun `syncOnce 移除已消失的受管服务器`() {
        val current = mutableListOf(instance("lobby-1", "10.0.0.7:25565"))
        val directory = FakeDirectory()
        val syncer = ProxyServerDirectorySyncer(directory) { current.toList() }
        syncer.syncOnce()

        current.clear()
        syncer.syncOnce()

        assertTrue(!directory.managed.contains("lobby-1"))
    }

    @Test
    fun `syncOnce 只同步在线 bukkit 实例`() {
        val directory = FakeDirectory()
        val syncer = ProxyServerDirectorySyncer(directory) {
            listOf(
                instance("lobby-1", "10.0.0.7:25565", role = "bukkit", status = "online"),
                instance("proxy-2", "10.0.0.8:25577", role = "bungee", status = "online"),
                instance("lost-1", "10.0.0.9:25565", role = "bukkit", status = "lost"),
            )
        }

        syncer.syncOnce()

        assertEquals(setOf("lobby-1"), directory.managed)
    }

    @Test
    fun `syncOnce 据 home-zone 命中默认入口设默认服`() {
        val directory = FakeDirectory()
        val warnings = mutableListOf<String>()
        val syncer = ProxyServerDirectorySyncer(
            directory,
            homeGroup = "area1",
            homeZone = "zoneA",
            warn = warnings::add,
        ) {
            listOf(
                instance("lobby-1", "10.0.0.7:25565", defaultEntry = false),
                instance("lobby-2", "10.0.0.8:25565", defaultEntry = true),
            )
        }

        syncer.syncOnce()

        // 命中 home-zone 的默认入口 lobby-2 被设为默认服，不告警
        assertEquals("lobby-2", directory.capturedDefault)
        assertTrue(warnings.isEmpty())
    }

    @Test
    fun `syncOnce 未配 home-zone 时不设默认服并告警`() {
        val directory = FakeDirectory()
        val warnings = mutableListOf<String>()
        val syncer = ProxyServerDirectorySyncer(directory, warn = warnings::add) {
            listOf(
                instance("lobby-1", "10.0.0.7:25565", defaultEntry = false),
                instance("lobby-2", "10.0.0.8:25565", defaultEntry = true),
            )
        }

        syncer.syncOnce()

        // 未配 home-zone：绝不回退到任意在线 bukkit，不设默认服 + 打一条 WARN
        assertNull(directory.capturedDefault)
        assertEquals(0, directory.setDefaultCalls)
        assertTrue(warnings.single().contains("未配"))
    }

    @Test
    fun `syncOnce 配了 home-zone 但该 zone 无默认入口时不设默认服并告警`() {
        val directory = FakeDirectory()
        val warnings = mutableListOf<String>()
        val syncer = ProxyServerDirectorySyncer(
            directory,
            homeGroup = "area1",
            homeZone = "zoneA",
            warn = warnings::add,
        ) {
            // 在线 bukkit 命中 home-zone 但均未被标默认入口
            listOf(instance("lobby-1", "10.0.0.7:25565", defaultEntry = false))
        }

        syncer.syncOnce()

        assertNull(directory.capturedDefault)
        assertEquals(0, directory.setDefaultCalls)
        assertTrue(warnings.single().contains("area1/zoneA"))
    }

    @Test
    fun `syncOnce 默认入口离线时不设默认服并告警`() {
        val directory = FakeDirectory()
        val warnings = mutableListOf<String>()
        val syncer = ProxyServerDirectorySyncer(
            directory,
            homeGroup = "area1",
            homeZone = "zoneA",
            warn = warnings::add,
        ) {
            listOf(instance("lobby-1", "10.0.0.7:25565", status = "lost", defaultEntry = true))
        }

        syncer.syncOnce()

        // 默认入口虽配但当前 lost（不在线）：不设默认服 + 告警
        assertNull(directory.capturedDefault)
        assertTrue(warnings.single().contains("area1/zoneA"))
    }

    @Test
    fun `syncOnce 选不出默认服时多轮只告警一次`() {
        val directory = FakeDirectory()
        val warnings = mutableListOf<String>()
        val syncer = ProxyServerDirectorySyncer(directory, warn = warnings::add) {
            listOf(instance("lobby-1", "10.0.0.7:25565", defaultEntry = false))
        }

        syncer.syncOnce()
        syncer.syncOnce()
        syncer.syncOnce()

        // 连续选不出默认服：WARN 去重、不每轮刷屏
        assertEquals(1, warnings.size)
    }

    @Test
    fun `syncOnce 默认服不变时不重复设置`() {
        val directory = FakeDirectory()
        val syncer = ProxyServerDirectorySyncer(directory, homeGroup = "area1", homeZone = "zoneA") {
            listOf(instance("lobby-1", "10.0.0.7:25565", defaultEntry = true))
        }

        syncer.syncOnce()
        syncer.syncOnce()

        // 两轮选出的默认服相同，只设一次（去重）
        assertEquals(1, directory.setDefaultCalls)
        assertEquals("lobby-1", directory.capturedDefault)
    }

    private fun instance(
        serverId: String,
        address: String,
        role: String = "bukkit",
        status: String = "online",
        defaultEntry: Boolean = false,
    ): ServiceInstance {
        return ServiceInstance(serverId, role, "area1", "zoneA", address, "1.0", status, 0, 200, 100, defaultEntry)
    }

    private class FakeDirectory(
        val manual: MutableSet<String> = mutableSetOf(),
    ) : ProxyServerDirectory {
        val managed: MutableSet<String> = mutableSetOf()
        val addresses: MutableMap<String, String> = mutableMapOf()
        var capturedDefault: String? = null
        var setDefaultCalls: Int = 0

        override fun hasServer(serverId: String): Boolean = manual.contains(serverId) || managed.contains(serverId)

        override fun isManaged(serverId: String): Boolean = managed.contains(serverId)

        override fun upsertManaged(instance: ServiceInstance): Boolean {
            if (manual.contains(instance.serverId())) return false
            managed.add(instance.serverId())
            addresses[instance.serverId()] = instance.address()
            return true
        }

        override fun removeManaged(serverId: String) {
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
