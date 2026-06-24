package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.BackoffSettings
import top.wcpe.beacon.agent.core.settings.FileTreeSettings
import top.wcpe.beacon.agent.core.settings.OverrideSettings
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.util.concurrent.atomic.AtomicReference
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse

/**
 * BeaconApiClient register 携带 agent 自身构建版本（agentVersion）的报文契约单测（FR-86，见 ADR-0039）。
 *
 * 重点：agentVersion 是 agent 构建版本（壳层经 TabooLib pluginVersion 注入身份，非业务 version）；
 * 仅当非空时才拼入报文键（旧 agent / 未注入版本传空、向后兼容）；键名固定为 agentVersion（供控制面 Go 侧对齐）。
 */
class BeaconApiClientAgentVersionTest {

    /** 捕获 encode 入参的 codec：把待序列化的 Map 暴露给断言。 */
    private class CapturingCodec : JsonCodec {
        val lastEncoded = AtomicReference<Any?>(null)

        override fun encode(value: Any?): String {
            lastEncoded.set(value)
            return "captured"
        }

        // register 解析需要一个最小响应对象。
        override fun decode(json: String): Any? = mapOf(
            "instanceKey" to "prod/lobby-1",
            "heartbeatIntervalSec" to 10,
            "ttlSec" to 30,
            "assigned" to false,
        )
    }

    private class OkTransport : HttpTransport {
        override fun execute(request: HttpRequest): HttpResponse = HttpResponse(200, "")
    }

    private fun identity(agentVersion: String) = AgentIdentity(
        namespace = "prod",
        serverId = "lobby-1",
        role = "bukkit",
        groupHint = "area1",
        address = "127.0.0.1:25565",
        version = "1.0",
        capacity = 0,
        weight = 1,
        metadata = emptyMap(),
        agentVersion = agentVersion,
    )

    private fun settings() = AgentSettings(
        endpoints = listOf("http://localhost:8848"),
        bootstrapToken = "tk",
        pollTimeoutMs = 50,
        requestTimeoutMs = 200,
        heartbeatFallbackMs = 100_000,
        backoff = BackoffSettings(initialMs = 1000, maxMs = 1000, multiplier = 1.0, jitterRatio = 0.0),
        snapshotEnabled = false,
        snapshotFileName = "snapshot.json",
        fileTree = FileTreeSettings(enabled = false, targetSubDir = "", appliedManifestFileName = "file-tree.applied.json"),
        override = OverrideSettings(commandWhitelist = emptySet(), backupDirName = "override-backup"),
    )

    @Suppress("UNCHECKED_CAST")
    private fun lastBody(codec: CapturingCodec): Map<String, Any?> =
        codec.lastEncoded.get() as Map<String, Any?>

    @Test
    fun `register 携带非空 agentVersion`() {
        val codec = CapturingCodec()
        val client = BeaconApiClient(OkTransport(), codec, settings())

        client.register(identity(agentVersion = "0.12.0"))

        val body = lastBody(codec)
        assertEquals("0.12.0", body["agentVersion"])
    }

    @Test
    fun `register 在 agentVersion 为空时不拼键`() {
        val codec = CapturingCodec()
        val client = BeaconApiClient(OkTransport(), codec, settings())

        // 空 agentVersion（旧 agent / 未注入构建版本）。
        client.register(identity(agentVersion = ""))

        val body = lastBody(codec)
        assertFalse(body.containsKey("agentVersion"), "空 agentVersion 不应拼入报文（向后兼容旧控制面/旧 agent）")
    }
}
