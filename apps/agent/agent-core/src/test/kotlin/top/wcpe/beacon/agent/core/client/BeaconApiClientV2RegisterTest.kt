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

    /**
     * 构造「本控制面的 JSON 应答」——真实控制面（render.WriteJSON）一律带
     * `Content-Type: application/json`，插件据此识别应答归属（FR-233 回退判据）。
     */
    private fun jsonResponse(
        status: Int,
        body: String,
    ) = HttpResponse(
        statusCode = status,
        body = body,
        contentType = "application/json; charset=utf-8",
    )

    /**
     * 构造「老控制面的 SPA 兜底应答」——未匹配路径由内嵌前端接管，回 200 + text/html。
     * 这是「对端不认识新路径」的真实形态之一（另一种为无前端时的 404 + text/plain）。
     */
    private fun spaFallbackResponse() =
        HttpResponse(
            statusCode = 200,
            body = "<!doctype html><html></html>",
            contentType = "text/html; charset=utf-8",
        )

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
        val transport = ScriptedTransport(ArrayDeque(listOf(jsonResponse(202, "v2-pending"))))
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
                        jsonResponse(200, "v2-active"),
                        jsonResponse(200, "legacy-ok"),
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
        val transport = ScriptedTransport(ArrayDeque(listOf(jsonResponse(202, "v2-pending"))))
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
        val transport = ScriptedTransport(ArrayDeque(listOf(jsonResponse(202, "v2-pending"))))
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
        val transport = ScriptedTransport(ArrayDeque(listOf(jsonResponse(202, "v2-pending"))))

        BeaconApiClient(transport, codec, settings()).register(identity().copy(role = "bungee"))

        val body = codec.encoded.single() as Map<String, Any?>
        assertEquals(emptyList<Any>(), body["listeners"])
    }

    @Suppress("UNCHECKED_CAST")
    @Test
    fun `Bukkit 缺少监听端口时仍显式上报无效值供控制面拒绝`() {
        val codec = CapturingCodec()
        val transport = ScriptedTransport(ArrayDeque(listOf(jsonResponse(202, "v2-pending"))))

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
                        jsonResponse(200, "v2-active"),
                        jsonResponse(200, "legacy-ok"),
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
        // 老控制面（尚未支持新路径）+ 无前端产物：回 404 + text/plain，插件须改用旧路径重试一次。
        val transport =
            ScriptedTransport(
                ArrayDeque(
                    listOf(
                        jsonResponse(200, "v2-active"),
                        HttpResponse(404, "404 page not found", "text/plain; charset=utf-8"),
                        jsonResponse(200, "legacy-ok"),
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
    fun `新路径被 SPA 兜底回 200 HTML 时同样回退旧兼容路径`() {
        // 老控制面 + **有前端产物**（正式构建的常见形态）：未匹配路径交给内嵌前端兜底，
        // 回的是 200 + text/html 而非 404——只看状态码会判定「成功」并把 HTML 当 JSON 解析。
        // 本用例锁定「按响应是否 JSON 判定」这一修复：去掉判据即会拿 HTML 当成功而红。
        val transport =
            ScriptedTransport(
                ArrayDeque(
                    listOf(
                        jsonResponse(200, "v2-active"),
                        spaFallbackResponse(),
                        jsonResponse(200, "legacy-ok"),
                    ),
                ),
            )
        val outcome = BeaconApiClient(transport, CapturingCodec(), settings()).register(identity())

        val success = assertIs<RegisterOutcome.Success>(outcome)
        assertEquals("prod/lobby-1", success.result.instanceKey)
        assertEquals(3, transport.requests.size)
        assertTrue(transport.requests[2].url.endsWith("/beacon/v1/agent/register"))
    }

    @Test
    fun `回退后仍回非 JSON 时兜底为 Failed 而非抛异常`() {
        // 极端形态：两条路径都被非 JSON 应答接管（如中间层返回 HTML 错误页）。
        // 解析 HTML 会抛异常，必须兜底转 Failed 交上层退避，绝不能逃逸打断注册循环。
        val transport =
            ScriptedTransport(
                ArrayDeque(
                    listOf(
                        jsonResponse(200, "v2-active"),
                        spaFallbackResponse(),
                        spaFallbackResponse(),
                    ),
                ),
            )
        val outcome = BeaconApiClient(transport, CapturingCodec(), settings()).register(identity())

        assertIs<RegisterOutcome.Failed>(outcome)
        assertEquals(3, transport.requests.size)
    }

    @Test
    fun `回退后的响应仍按既有状态码映射返回`() {
        // 路径选择只决定「走哪条路径」；回退后的状态码一律按既有映射处理，不得再触发任何重试。
        val transport =
            ScriptedTransport(
                ArrayDeque(
                    listOf(
                        jsonResponse(200, "v2-active"),
                        HttpResponse(404, "404 page not found", "text/plain; charset=utf-8"),
                        jsonResponse(409, "duplicated"),
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
                            jsonResponse(200, "v2-active"),
                            // 业务错误应答同样是本控制面的 JSON（render.WriteJSON），
                            // 故判据不得把它当作「对端不认识路径」而回退。
                            jsonResponse(statusCode, "rejected"),
                            // 多备一条：若实现错误地回退，请求次数断言会先失败，而非在此静默成功。
                            jsonResponse(200, "legacy-ok"),
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
                    return jsonResponse(200, "v2-active")
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
