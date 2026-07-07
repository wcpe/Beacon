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
}
