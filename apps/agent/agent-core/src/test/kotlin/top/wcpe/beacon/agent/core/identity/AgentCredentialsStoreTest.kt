package top.wcpe.beacon.agent.core.identity

import top.wcpe.beacon.agent.core.testsupport.TestJsonCodec
import java.nio.file.Files
import java.nio.file.Path
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * 身份凭据存储的测试（FR-276）。
 *
 * 重点覆盖三类**会静默出错**的场景：
 *  1. 凭据读写往返丢失字段（token 含特殊字符时尤甚）
 *  2. 损坏/未知版本文件被"猜着读"，把坏数据当成有效凭据
 *  3. 旧 identity.yml 迁移时身份漂移（会让控制面多出一个重复身份）
 */
class AgentCredentialsStoreTest {
    private fun tempDir(): Path = Files.createTempDirectory("beacon-cred-test")

    @Test
    fun `凭据写入后可原样读回`() {
        val dir = tempDir()
        val store = AgentCredentialsStore(dir, TestJsonCodec)
        val cred =
            AgentCredentials(
                identityId = "0f8fad5b-d9cb-469f-a165-70867728950e",
                endpoint = "https://beacon.example.com",
                token = "bn_abc123",
            )

        store.save(cred)
        val loaded = store.load()

        assertNotNull(loaded)
        assertEquals(cred.identityId, loaded.identityId)
        assertEquals(cred.endpoint, loaded.endpoint)
        assertEquals(cred.token, loaded.token)
        assertNull(loaded.binding)
        assertTrue(loaded.canConnect, "地址与令牌都在，应可直连")
    }

    @Test
    fun `令牌含引号与反斜杠也能原样读回`() {
        val dir = tempDir()
        val store = AgentCredentialsStore(dir, TestJsonCodec)
        // 令牌里出现引号/反斜杠在真实场景里少见，但一旦出现必须不破坏 JSON 结构，
        // 否则解析会静默截断——表现为「token 莫名其妙变短」。
        val tricky = """bn_a"b\c"""
        store.save(
            AgentCredentials(
                identityId = "0f8fad5b-d9cb-469f-a165-70867728950e",
                endpoint = "http://127.0.0.1:20020",
                token = tricky,
            ),
        )

        val loaded = store.load()
        assertNotNull(loaded)
        assertEquals(tricky, loaded.token)
    }

    @Test
    fun `绑定事实可单独更新且不影响地址与令牌`() {
        val dir = tempDir()
        val store = AgentCredentialsStore(dir, TestJsonCodec)
        store.save(
            AgentCredentials("0f8fad5b-d9cb-469f-a165-70867728950e", "https://b.example.com", "bn_x"),
        )

        val updated =
            store.updateBinding(
                AgentCredentials.BoundIdentity(
                    namespace = "prod",
                    serverId = "lobby-1",
                    kind = "bukkit",
                    address = "127.0.0.1:25565",
                    boundAt = null,
                ),
            )

        assertNotNull(updated)
        assertEquals("prod", updated.binding?.namespace)
        assertEquals("lobby-1", updated.binding?.serverId)
        // 关键：更新绑定不得把地址/令牌清空（那会让下次启动无法接入）。
        assertEquals("https://b.example.com", updated.endpoint)
        assertEquals("bn_x", updated.token)
    }

    @Test
    fun `损坏文件被判为无凭据而不是读出半截值`() {
        val dir = tempDir()
        Files.writeString(dir.resolve("agent-credentials.json"), "{ 这不是合法 JSON")
        val store = AgentCredentialsStore(dir, TestJsonCodec)

        // 关键：宁可当作「没凭据」让流程走 join，也不能读出半截数据把错误地址当成真的。
        assertNull(store.load())
    }

    @Test
    fun `未知格式版本被判为无凭据`() {
        val dir = tempDir()
        Files.writeString(
            dir.resolve("agent-credentials.json"),
            """{"formatVersion": 99, "identityId": "0f8fad5b-d9cb-469f-a165-70867728950e", "endpoint": "http://a", "token": "t"}""",
        )
        val store = AgentCredentialsStore(dir, TestJsonCodec)

        assertNull(store.load(), "未来版本的文件不该被旧 agent 猜着读")
    }

    @Test
    fun `旧身份文件迁移后身份标识不变`() {
        val dir = tempDir()
        val store = AgentCredentialsStore(dir, TestJsonCodec)
        val legacyId = "1f8fad5b-d9cb-469f-a165-70867728950e"

        val migrated = store.migrateFromLegacy(StoredAgentIdentity(legacyId, null, isValid = true))

        assertNotNull(migrated)
        // 身份不漂移是迁移的唯一目的：漂了就等于控制面上多一台新服务器。
        assertEquals(legacyId, migrated.identityId)
        assertFalse(migrated.canConnect, "迁移只搬身份，地址与令牌要等 join 或 config.yml")
    }

    @Test
    fun `已有凭据时不重复迁移`() {
        val dir = tempDir()
        val store = AgentCredentialsStore(dir, TestJsonCodec)
        store.save(
            AgentCredentials("0f8fad5b-d9cb-469f-a165-70867728950e", "https://a", "bn_a"),
        )

        val result =
            store.migrateFromLegacy(
                StoredAgentIdentity("1f8fad5b-d9cb-469f-a165-70867728950e", null, isValid = true),
            )

        assertNull(result, "已有凭据时不得用旧身份覆盖")
        assertEquals("0f8fad5b-d9cb-469f-a165-70867728950e", store.load()?.identityId)
    }
}
