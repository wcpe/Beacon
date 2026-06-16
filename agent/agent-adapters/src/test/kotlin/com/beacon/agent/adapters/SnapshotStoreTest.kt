package com.beacon.agent.adapters

import com.beacon.agent.core.config.ConfigItem
import com.beacon.agent.core.config.EffectiveResult
import com.beacon.agent.core.snapshot.SnapshotStore
import java.io.File
import java.nio.file.Files
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue

/** SnapshotStore 读写往返一致性（用真实 KotlinxJsonCodec）。 */
class SnapshotStoreTest {

    private val codec = KotlinxJsonCodec()
    private val dir: File = Files.createTempDirectory("beacon-snap").toFile()

    @AfterTest
    fun cleanup() {
        dir.deleteRecursively()
    }

    private fun store() = SnapshotStore(File(dir, "snap.json"), codec)

    @Test
    fun `写后读回与原值一致`() {
        val result = EffectiveResult(
            namespace = "prod",
            serverId = "lobby-1",
            group = "area1",
            zone = "zoneA",
            md5 = "abc123",
            items = listOf(
                ConfigItem("mysql.yml", "yaml", "9f", "url: jdbc:mysql\npool: 20\n"),
                ConfigItem("merge.json", "json", "77", "{\"area1\":[\"zoneA\"]}"),
            ),
        )
        val store = store()
        store.write(result)
        val read = store.read()!!

        assertEquals(result.namespace, read.namespace)
        assertEquals(result.serverId, read.serverId)
        assertEquals(result.group, read.group)
        assertEquals(result.zone, read.zone)
        assertEquals(result.md5, read.md5)
        assertEquals(result.items, read.items)
    }

    @Test
    fun `zone 为 null 往返保持 null`() {
        val result = EffectiveResult("prod", "lobby-1", "area1", null, "m", emptyList())
        val store = store()
        store.write(result)
        assertNull(store.read()!!.zone)
    }

    @Test
    fun `文件不存在时读返回 null`() {
        assertNull(store().read())
    }

    @Test
    fun `写入采用原子替换不残留 tmp`() {
        val store = store()
        store.write(EffectiveResult("prod", "s", "g", "z", "m", emptyList()))
        val tmp = File(dir, "snap.json.tmp")
        assertTrue(!tmp.exists(), "tmp 文件应在原子重命名后消失")
    }
}
