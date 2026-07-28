package top.wcpe.beacon.agent.core.identity

import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.nio.file.Files
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import top.wcpe.beacon.agent.core.client.ActiveBinding

class IdentityBindingSnapshotStoreTest {
    @Test
    fun `只接受同一 identity 与角色的绑定快照`() {
        val file = Files.createTempFile("beacon-binding", ".json").toFile()
        val codec = FixedCodec()
        val store = IdentityBindingSnapshotStore(file, codec)
        val identity = identity()

        val binding = store.load(identity)

        assertEquals("prod", binding?.namespace)
        assertEquals("lobby-1", binding?.serverId)
        assertNull(store.load(identity.copy(identityId = "other")))
        assertNull(store.load(identity.copy(role = "bungee")))
    }

    @Test
    fun `格式版本、确认时间或控制面摘要缺失时快照失效`() {
        val file = Files.createTempFile("beacon-binding-invalid", ".json").toFile()
        val store =
            IdentityBindingSnapshotStore(
                file,
                object : JsonCodec {
                    override fun encode(value: Any?): String = "{}"

                    override fun decode(json: String): Any? = mapOf("formatVersion" to 1)
                },
            )

        assertNull(store.load(identity()))
    }

    @Test
    fun `旧快照缺少兼容地址时拒绝启动`() {
        val file = Files.createTempFile("beacon-binding-old", ".json").toFile()
        val store = IdentityBindingSnapshotStore(file, FixedCodec(includeAddress = false))

        assertNull(store.load(identity()))
    }

    @Test
    fun `地址变化不改变控制面绑定事实`() {
        val file = Files.createTempFile("beacon-binding-address", ".json").toFile()
        val store = IdentityBindingSnapshotStore(file, FixedCodec())
        val cached = store.load(identity())!!
        val current = ActiveBinding("prod", "lobby-1", cached.boundAt, cached.bindingFingerprint, "203.0.113.8:25565")

        assertEquals(cached.namespace, current.namespace)
        assertEquals(cached.bindingFingerprint, current.bindingFingerprint)
    }

    private fun identity() =
        AgentIdentity(
            namespace = "",
            serverId = "",
            role = "bukkit",
            groupHint = "",
            address = "",
            version = "",
            capacity = 0,
            weight = 0,
            metadata = emptyMap(),
            identityId = "identity-a",
            bootId = "boot-a",
        )

    private class FixedCodec(private val includeAddress: Boolean = true) : JsonCodec {
        override fun encode(value: Any?): String = "{}"

        override fun decode(json: String): Any? =
            buildMap {
                putAll(mapOf(
                "formatVersion" to 1,
                "identityId" to "identity-a",
                "kind" to "bukkit",
                "namespace" to "prod",
                "serverId" to "lobby-1",
                "boundAt" to "2026-07-28T12:00:00Z",
                "bindingFingerprint" to "a".repeat(64),
                ))
                if (includeAddress) put("address", "203.0.113.10:25565")
            }
    }
}
