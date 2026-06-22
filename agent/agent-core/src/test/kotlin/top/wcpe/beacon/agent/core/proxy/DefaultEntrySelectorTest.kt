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
    fun `home-zone 未配时兜底取首个在线 bukkit`() {
        val instances = listOf(
            bukkit("lobby-1", defaultEntry = false),
            bukkit("lobby-2", defaultEntry = true),
        )
        assertEquals("lobby-1", DefaultEntrySelector.select(instances, "", ""))
    }

    @Test
    fun `home-zone 配了但无命中默认入口时兜底首个在线 bukkit`() {
        val instances = listOf(
            bukkit("lobby-1", group = "area1", zone = "zoneA", defaultEntry = false),
            // 另一 zone 的默认入口，不命中 home-zone
            bukkit("lobby-2", group = "area2", zone = "zoneB", defaultEntry = true),
        )
        assertEquals("lobby-1", DefaultEntrySelector.select(instances, "area1", "zoneA"))
    }

    @Test
    fun `只看在线 bukkit 忽略 bungee 与非在线`() {
        val instances = listOf(
            bukkit("lost-1", status = "lost", defaultEntry = true),
            ServiceInstance("bc-1", "bungee", "area1", "zoneA", "10.0.0.9:25577", "1.0", "online", 0, 0, 0, false),
            bukkit("lobby-2", defaultEntry = false),
        )
        // lost-1 虽标默认入口但非在线，bc-1 非 bukkit；兜底取首个在线 bukkit lobby-2
        assertEquals("lobby-2", DefaultEntrySelector.select(instances, "", ""))
    }

    @Test
    fun `无在线 bukkit 时返回 null`() {
        val instances = listOf(
            bukkit("lost-1", status = "lost"),
            ServiceInstance("bc-1", "bungee", "area1", "zoneA", "10.0.0.9:25577", "1.0", "online", 0, 0, 0, false),
        )
        assertNull(DefaultEntrySelector.select(instances, "area1", "zoneA"))
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
