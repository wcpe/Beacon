package top.wcpe.beacon.agent.core.identity

import java.nio.file.Files
import java.util.UUID
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotNull
import kotlin.test.assertTrue

class AgentIdentityStoreTest {
    @Test
    fun `首次读取会生成 identity 文件且后续读取保持不变`() {
        val dir = Files.createTempDirectory("beacon-identity-store")
        val store = AgentIdentityStore(dir)

        val first = store.loadOrCreate()
        val second = store.loadOrCreate()

        assertEquals(first.identityId, second.identityId)
        assertNotNull(UUID.fromString(first.identityId))
        val content = Files.readString(dir.resolve("identity.yml"))
        assertTrue(content.contains("identity-id:"))
        assertTrue(content.contains("format-version: 1"))
    }

    @Test
    fun `身份文件损坏时不自动生成新身份`() {
        val dir = Files.createTempDirectory("beacon-identity-corrupt")
        val file = dir.resolve("identity.yml")
        Files.writeString(file, "format-version: 1\nidentity-id: not-a-uuid\n")
        val before = Files.readString(file)

        val result = AgentIdentityStore(dir).loadExisting()

        assertFalse(result.isValid)
        assertEquals(before, Files.readString(file))
    }

    /**
     * 全新安装时身份文件必然不存在，这是**正常态**（等 join 即可），不是损坏。
     *
     * 这条锁的存在理由：壳层曾用 `!isValid && 凭据为空` 判「身份损坏」并 fail-fast，
     * 结果全新机器上 `loadExisting()` 返回 isValid=false → 直接拒绝接入，
     * 零配置接入被彻底堵死（真机实测表现为「身份文件损坏：身份文件不存在」）。
     * 故 must 区分「不存在」与「存在但读不懂」。
     */
    @Test
    fun `文件不存在时标记为不存在而不是损坏`() {
        val dir = Files.createTempDirectory("beacon-identity-absent")

        val result = AgentIdentityStore(dir).loadExisting()

        assertFalse(result.isValid)
        assertFalse(result.fileExists, "文件不存在必须是 fileExists=false，否则壳层会误判成损坏")
        assertTrue(result.identityId.isEmpty())
    }

    /** 文件存在但内文无法解析（标签为损坏）时，必须有 fileExists=true 让壳层能 fail-fast。 */
    @Test
    fun `文件存在但内容损坏时标记为存在`() {
        val dir = Files.createTempDirectory("beacon-identity-corrupt-exists")
        Files.writeString(dir.resolve("identity.yml"), "format-version: 1\nidentity-id: not-a-uuid\n")

        val result = AgentIdentityStore(dir).loadExisting()

        assertFalse(result.isValid)
        assertTrue(result.fileExists, "存在但读不懂才是真损坏，必须让壳层能看见")
        assertFalse(result.error.isEmpty())
    }

    /** 正常身份文件的 fileExists 同样为 true（它确实存在于磁盘）。 */
    @Test
    fun `正常身份文件标记为存在且有效`() {
        val dir = Files.createTempDirectory("beacon-identity-ok")
        val created = AgentIdentityStore(dir).loadOrCreate()

        val result = AgentIdentityStore(dir).loadExisting()

        assertTrue(result.isValid)
        assertTrue(result.fileExists)
        assertEquals(created.identityId, result.identityId)
    }
}
