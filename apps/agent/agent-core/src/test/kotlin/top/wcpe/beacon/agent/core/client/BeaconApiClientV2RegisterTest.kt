package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.identity.EndpointReport
import top.wcpe.beacon.agent.core.identity.ProxyListenerEndpoint
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
                "v2-active" ->
                    mapOf(
                        "status" to "active",
                        "namespace" to "prod",
                        "serverId" to "lobby-1",
                        "boundAt" to "2026-07-28T12:00:00Z",
                        "bindingFingerprint" to "a".repeat(64),
                        "address" to "203.0.113.10:25565",
                    )
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

    @Suppress("UNCHECKED_CAST")
    @Test
    fun `新引导注册不发送本地 serverId 且从 active 响应采用权威绑定`() {
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
        val bootstrapIdentity = identity().copy(namespace = "", serverId = "")

        BeaconApiClient(transport, codec, settings()).register(bootstrapIdentity)

        val v2Body = codec.encoded.first() as Map<String, Any?>
        assertTrue("serverId" !in v2Body)
        assertEquals("prod", bootstrapIdentity.namespace)
        assertEquals("lobby-1", bootstrapIdentity.serverId)
        assertEquals("203.0.113.10:25565", bootstrapIdentity.address)
    }

    @Suppress("UNCHECKED_CAST")
    @Test
    fun `Bukkit 注册只上报监听端口且不发送本地地址`() {
        val codec = CapturingCodec()
        val transport = ScriptedTransport(ArrayDeque(listOf(HttpResponse(202, "v2-pending"))))
        val identity = identity().copy(address = "10.0.0.8:25565", endpointReport = EndpointReport(backendListenPort = 25565))

        BeaconApiClient(transport, codec, settings()).register(identity)

        val body = codec.encoded.single() as Map<String, Any?>
        assertEquals(25565, body["listenPort"])
        assertTrue("addr" !in body)
        assertTrue("address" !in body)
    }

    @Suppress("UNCHECKED_CAST")
    @Test
    fun `BC 注册上报全部 listener 并保持 ordinal 顺序`() {
        val codec = CapturingCodec()
        val transport = ScriptedTransport(ArrayDeque(listOf(HttpResponse(202, "v2-pending"))))
        val identity =
            identity().copy(
                role = "bungee",
                endpointReport =
                    EndpointReport(
                        proxyListeners =
                            listOf(
                                ProxyListenerEndpoint("127.0.0.1", 25577, 1),
                                ProxyListenerEndpoint("0.0.0.0", 25565, 0),
                            ),
                    ),
            )

        BeaconApiClient(transport, codec, settings()).register(identity)

        val body = codec.encoded.single() as Map<String, Any?>
        val listeners = body["listeners"] as List<Map<String, Any?>>
        assertEquals(listOf(0, 1), listeners.map { it["ordinal"] })
        assertEquals(listOf(25565, 25577), listeners.map { it["port"] })
        assertTrue("addr" !in body)
    }

    @Suppress("UNCHECKED_CAST")
    @Test
    fun `BC 缺少 listener 时仍显式上报空列表供控制面拒绝`() {
        val codec = CapturingCodec()
        val transport = ScriptedTransport(ArrayDeque(listOf(HttpResponse(202, "v2-pending"))))

        BeaconApiClient(transport, codec, settings()).register(identity().copy(role = "bungee"))

        val body = codec.encoded.single() as Map<String, Any?>
        assertEquals(emptyList<Any>(), body["listeners"])
    }

    @Suppress("UNCHECKED_CAST")
    @Test
    fun `Bukkit 缺少监听端口时仍显式上报无效值供控制面拒绝`() {
        val codec = CapturingCodec()
        val transport = ScriptedTransport(ArrayDeque(listOf(HttpResponse(202, "v2-pending"))))

        BeaconApiClient(transport, codec, settings()).register(identity().copy(endpointReport = EndpointReport()))

        val body = codec.encoded.single() as Map<String, Any?>
        assertEquals(0, body["listenPort"])
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
        assertTrue(transport.requests[1].url.endsWith("/beacon/v1/agent/data-plane/attach"))
        assertEquals(identity().identityId, transport.requests[1].headers[BeaconApiClient.HEADER_IDENTITY])
        assertEquals(identity().bootId, transport.requests[1].headers[BeaconApiClient.HEADER_BOOT])
    }

    @Test
    fun `新路径 404 时回退旧兼容路径重试一次并成功`() {
        // 旧控制面（尚未支持新路径）对 /data-plane/attach 回 404，插件须改用旧路径重试一次。
        val transport =
            ScriptedTransport(
                ArrayDeque(
                    listOf(
                        HttpResponse(200, "v2-active"),
                        HttpResponse(404, "not found"),
                        HttpResponse(200, "legacy-ok"),
                    ),
                ),
            )
        val outcome = BeaconApiClient(transport, CapturingCodec(), settings()).register(identity())

        val success = assertIs<RegisterOutcome.Success>(outcome)
        assertEquals("prod/lobby-1", success.result.instanceKey)
        assertEquals(3, transport.requests.size)
        assertTrue(transport.requests[1].url.endsWith("/beacon/v1/agent/data-plane/attach"))
        assertTrue(transport.requests[2].url.endsWith("/beacon/v1/agent/register"))
        // 回退仅改路径：报文与请求头逐字一致，语义等同新路径。
        assertEquals(transport.requests[1].body, transport.requests[2].body)
        assertEquals(transport.requests[1].headers, transport.requests[2].headers)
    }

    @Test
    fun `回退后的响应仍按既有状态码映射返回`() {
        // 404 只决定「走哪条路径」；回退后的状态码一律按既有映射处理，不得再触发任何重试。
        val transport =
            ScriptedTransport(
                ArrayDeque(
                    listOf(
                        HttpResponse(200, "v2-active"),
                        HttpResponse(404, "not found"),
                        HttpResponse(409, "duplicated"),
                    ),
                ),
            )
        val outcome = BeaconApiClient(transport, CapturingCodec(), settings()).register(identity())

        assertEquals(RegisterOutcome.DuplicateServerId, outcome)
        assertEquals(3, transport.requests.size)
        assertTrue(transport.requests[2].url.endsWith("/beacon/v1/agent/register"))
    }

    @Test
    fun `新路径非 404 状态码一律不回退旧路径`() {
        // 200/400/401/403/409 都由真实业务语义产生：回退会掩盖真实错误
        // （例如此处 409 重复 serverId 被误当作「版本不匹配」而重试）。
        val cases =
            mapOf(
                409 to RegisterOutcome.DuplicateServerId,
                403 to RegisterOutcome.OfflineRejected,
                401 to RegisterOutcome.Unauthorized,
                400 to RegisterOutcome.IdentityRequired,
            )
        cases.forEach { (statusCode, expected) ->
            val transport =
                ScriptedTransport(
                    ArrayDeque(
                        listOf(
                            HttpResponse(200, "v2-active"),
                            HttpResponse(statusCode, "rejected"),
                            // 多备一条：若实现错误地回退，请求次数断言会先失败，而非在此静默成功。
                            HttpResponse(200, "legacy-ok"),
                        ),
                    ),
                )

            val outcome = BeaconApiClient(transport, CapturingCodec(), settings()).register(identity())

            assertEquals(expected, outcome, "状态码 $statusCode 应按既有映射返回")
            assertEquals(2, transport.requests.size, "状态码 $statusCode 不得触发回退重试")
            assertTrue(transport.requests[1].url.endsWith("/beacon/v1/agent/data-plane/attach"))
        }
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

    @Test
    fun `心跳 403 或 409 必须回查 v2 权威身份而非当作网络失败`() {
        listOf(403, 409).forEach { statusCode ->
            val transport = ScriptedTransport(ArrayDeque(listOf(HttpResponse(statusCode, "rejected"))))

            val outcome = BeaconApiClient(transport, CapturingCodec(), settings()).heartbeat(identity())

            assertIs<HeartbeatOutcome.AuthorityRefreshRequired>(outcome)
        }
    }
}
