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
import kotlin.test.assertIs
import kotlin.test.assertTrue

/** BeaconApiClient v2 注册契约测试。 */
class BeaconApiClientV2RegisterTest {
    private class CapturingCodec : JsonCodec {
        val encoded = mutableListOf<Any?>()

        override fun encode(value: Any?): String {
            encoded += value
            return "body-${encoded.size}"
        }

        override fun decode(json: String): Any? =
            when (json) {
                "v2-pending" -> mapOf("status" to "pending", "namespace" to "prod", "serverId" to "lobby-1")
                "v2-active" -> mapOf("status" to "active", "namespace" to "prod", "serverId" to "lobby-1")
                "legacy-ok" ->
                    mapOf(
                        "instanceKey" to "prod/lobby-1",
                        "resolvedGroup" to "area1",
                        "resolvedZone" to "zoneA",
                        "heartbeatIntervalSec" to 10,
                        "ttlSec" to 30,
                        "assigned" to true,
                    )

                else -> emptyMap<String, Any?>()
            }
    }

    private class ScriptedTransport(private val responses: ArrayDeque<HttpResponse>) : HttpTransport {
        val requests = mutableListOf<HttpRequest>()

        override fun execute(request: HttpRequest): HttpResponse {
            requests += request
            return responses.removeFirst()
        }
    }

    private fun settings() =
        AgentSettings(
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

    private fun identity() =
        AgentIdentity(
            namespace = "prod",
            serverId = "lobby-1",
            role = "bukkit",
            groupHint = "area1",
            address = "127.0.0.1:25565",
            version = "1.0",
            capacity = 100,
            weight = 1,
            metadata = emptyMap(),
            agentVersion = "0.21.0",
            identityId = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
            bootId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
        )

    @Suppress("UNCHECKED_CAST")
    @Test
    fun `v2 首次注册 pending 时不落 legacy 注册`() {
        val codec = CapturingCodec()
        val transport = ScriptedTransport(ArrayDeque(listOf(HttpResponse(202, "v2-pending"))))
        val outcome = BeaconApiClient(transport, codec, settings()).register(identity())

        assertIs<RegisterOutcome.PendingApproval>(outcome)
        assertEquals(1, transport.requests.size)
        val req = transport.requests.single()
        assertTrue(req.url.endsWith("/beacon/v2/agent/register"))
        assertEquals("tk", req.headers[BeaconApiClient.HEADER_TOKEN])
        val body = codec.encoded.single() as Map<String, Any?>
        assertEquals("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", body["identityId"])
        assertEquals("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", body["bootId"])
        assertEquals("backend", body["kind"])
    }

    @Test
    fun `v2 active 后衔接 legacy 数据面注册`() {
        val codec = CapturingCodec()
        val transport =
            ScriptedTransport(
                ArrayDeque(
                    listOf(
                        HttpResponse(200, "v2-active"),
                        HttpResponse(200, "legacy-ok"),
                    ),
                ),
            )
        val outcome = BeaconApiClient(transport, codec, settings()).register(identity())

        val success = assertIs<RegisterOutcome.Success>(outcome)
        assertEquals("prod/lobby-1", success.result.instanceKey)
        assertEquals(2, transport.requests.size)
        assertTrue(transport.requests[0].url.endsWith("/beacon/v2/agent/register"))
        assertTrue(transport.requests[1].url.endsWith("/beacon/v1/agent/register"))
        assertEquals(identity().identityId, transport.requests[1].headers[BeaconApiClient.HEADER_IDENTITY])
        assertEquals(identity().bootId, transport.requests[1].headers[BeaconApiClient.HEADER_BOOT])
    }

    @Test
    fun `registration 轮询用 identity header 查询当前状态`() {
        val lastRequest = AtomicReference<HttpRequest>()
        val transport =
            object : HttpTransport {
                override fun execute(request: HttpRequest): HttpResponse {
                    lastRequest.set(request)
                    return HttpResponse(200, "v2-active")
                }
            }
        val status = BeaconApiClient(transport, CapturingCodec(), settings()).pollRegistration(identity(), waitSeconds = 1)

        assertIs<RegistrationPollResult.Active>(status)
        val req = lastRequest.get()
        assertTrue(req.url.contains("/beacon/v2/agent/registration?wait=1"))
        assertEquals(identity().identityId, req.headers["X-Beacon-Identity"])
    }
}
