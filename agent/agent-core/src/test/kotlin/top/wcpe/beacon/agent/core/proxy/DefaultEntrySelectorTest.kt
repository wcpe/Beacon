package top.wcpe.beacon.agent.core.proxy

import top.wcpe.beacon.agent.api.ServiceInstance
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

class DefaultEntrySelectorTest {

    @Test
    fun `home-zone 命中默认入口时选该子服`() {
        val instances = listOf(
            bukkit("lobby-1", group = "area1", zone = "zoneA", defaultEntry = false),
            bukkit("lobby-2", group = "area1", zone = "zoneA", defaultEntry = true),
            bukkit("lobby-3", group = "area2", zone = "zoneB", defaultEntry = true),
        )
        assertEquals("lobby-2", DefaultEntrySelector.select(instances, "area1", "zoneA"))
    }

    @Test
    fun `home-zone 未配时不选默认服返回 null`() {
        val instances = listOf(
            bukkit("lobby-1", defaultEntry = false),
            bukkit("lobby-2", defaultEntry = true),
        )
        // 未配 home-zone：绝不回退到任意在线 bukkit，返回 null（调用方不设默认服 + 告警）。
        assertNull(DefaultEntrySelector.select(instances, "", ""))
    }

    @Test
    fun `home-zone 配了但无命中默认入口时返回 null`() {
        val instances = listOf(
            bukkit("lobby-1", group = "area1", zone = "zoneA", defaultEntry = false),
            // 另一 zone 的默认入口，不命中 home-zone
            bukkit("lobby-2", group = "area2", zone = "zoneB", defaultEntry = true),
        )
        // home-zone 配齐但该 zone 在 Beacon 未设默认入口：不回退，返回 null。
        assertNull(DefaultEntrySelector.select(instances, "area1", "zoneA"))
    }

    @Test
    fun `默认入口当前不在线时返回 null`() {
        val instances = listOf(
            // 命中 home-zone 的默认入口但已 lost（不在线）
            bukkit("lobby-1", group = "area1", zone = "zoneA", status = "lost", defaultEntry = true),
            bukkit("lobby-2", group = "area1", zone = "zoneA", defaultEntry = false),
        )
        // 默认入口离线：不回退到其它在线 bukkit，返回 null。
        assertNull(DefaultEntrySelector.select(instances, "area1", "zoneA"))
    }

    @Test
    fun `只认在线 bukkit 的默认入口忽略 bungee`() {
        val instances = listOf(
            // bungee 即便标默认入口、命中 home-zone 也不选（只认 bukkit）
            ServiceInstance("bc-1", "bungee", "area1", "zoneA", "10.0.0.9:25577", "1.0", "online", 0, 0, 0, true),
            bukkit("lobby-2", group = "area1", zone = "zoneA", defaultEntry = true),
        )
        assertEquals("lobby-2", DefaultEntrySelector.select(instances, "area1", "zoneA"))
    }

    private fun bukkit(
        serverId: String,
        group: String = "area1",
        zone: String = "zoneA",
        status: String = "online",
        defaultEntry: Boolean = false,
    ): ServiceInstance {
        return ServiceInstance(serverId, "bukkit", group, zone, "10.0.0.1:25565", "1.0", status, 0, 200, 100, defaultEntry)
    }
}
