package top.wcpe.beacon.agent.core.proxy

import top.wcpe.beacon.agent.api.ServiceInstance
import kotlin.test.Test
import kotlin.test.assertEquals
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
        assertTrue(warnings.single().contains("lobby-1"))
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

    private fun instance(
        serverId: String,
        address: String,
        role: String = "bukkit",
        status: String = "online",
    ): ServiceInstance {
        return ServiceInstance(serverId, role, "area1", "zoneA", address, "1.0", status, 0, 200, 100)
    }

    private class FakeDirectory(
        val manual: MutableSet<String> = mutableSetOf(),
    ) : ProxyServerDirectory {
        val managed: MutableSet<String> = mutableSetOf()
        val addresses: MutableMap<String, String> = mutableMapOf()

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
    }
}
