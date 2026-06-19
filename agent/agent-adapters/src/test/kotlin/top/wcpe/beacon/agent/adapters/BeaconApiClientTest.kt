package top.wcpe.beacon.agent.adapters

import top.wcpe.beacon.agent.adapters.testutil.FakeHttpTransport
import top.wcpe.beacon.agent.adapters.testutil.TestFixtures
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.HeartbeatOutcome
import top.wcpe.beacon.agent.core.client.PollResult
import top.wcpe.beacon.agent.core.client.RegisterOutcome
import top.wcpe.beacon.agent.core.transport.HttpResponse
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertIs
import kotlin.test.assertTrue

/**
 * BeaconApiClient 对假 transport + 真实 KotlinxJsonCodec 的集成单测：
 * register 解析、pollEffective 对 200/304/404 的映射、请求头 X-Beacon-Token。
 */
class BeaconApiClientTest {

    private val codec = KotlinxJsonCodec()

    private fun client(transport: FakeHttpTransport) =
        BeaconApiClient(transport, codec, TestFixtures.settings())

    @Test
    fun `register 200 解析 resolvedGroup zone 与 assigned`() {
        val transport = FakeHttpTransport().enqueue(
            HttpResponse(
                200,
                """{"instanceKey":"prod/lobby-1","resolvedGroup":"area1","resolvedZone":"zoneA",
                   "heartbeatIntervalSec":10,"ttlSec":30,"assigned":true}""".trimIndent(),
            ),
        )
        val outcome = client(transport).register(TestFixtures.identity())
        val success = assertIs<RegisterOutcome.Success>(outcome)
        assertEquals("area1", success.result.resolvedGroup)
        assertEquals("zoneA", success.result.resolvedZone)
        assertEquals(10, success.result.heartbeatIntervalSec)
        assertEquals(30, success.result.ttlSec)
        assertTrue(success.result.assigned)
    }

    @Test
    fun `register 请求体含顶层 capacity weight 与 metadata map 且头带 token`() {
        val transport = FakeHttpTransport().enqueue(
            HttpResponse(200, """{"instanceKey":"k","heartbeatIntervalSec":10,"ttlSec":30,"assigned":false}"""),
        )
        client(transport).register(TestFixtures.identity())

        val req = transport.captured.single()
        // 请求头携带正确的 X-Beacon-Token。
        assertEquals("test-token", req.headers["X-Beacon-Token"])
        // 请求体解析回泛型树校验字段位置：capacity/weight 顶层 int，metadata 为 map。
        @Suppress("UNCHECKED_CAST")
        val body = codec.decode(req.body!!) as Map<String, Any?>
        assertEquals(200L, body["capacity"])
        assertEquals(100L, body["weight"])
        @Suppress("UNCHECKED_CAST")
        val metadata = body["metadata"] as Map<String, Any?>
        assertEquals("cn-east", metadata["region"])
        // 全链路禁止 canary。
        assertTrue(!body.containsKey("canary"))
        assertTrue(!metadata.containsKey("canary"))
    }

    @Test
    fun `register 409 映射 DuplicateServerId`() {
        val transport = FakeHttpTransport().enqueue(HttpResponse(409, """{"code":"DUPLICATE_SERVER_ID"}"""))
        assertIs<RegisterOutcome.DuplicateServerId>(client(transport).register(TestFixtures.identity()))
    }

    @Test
    fun `register 401 映射 Unauthorized`() {
        val transport = FakeHttpTransport().enqueue(HttpResponse(401, ""))
        assertIs<RegisterOutcome.Unauthorized>(client(transport).register(TestFixtures.identity()))
    }

    @Test
    fun `pollEffective 200 返回 Changed 且解析 items 无 version`() {
        val transport = FakeHttpTransport().enqueue(
            HttpResponse(
                200,
                """{"namespace":"prod","serverId":"lobby-1","group":"area1","zone":"zoneA","md5":"abc",
                   "items":[{"dataId":"mysql.yml","format":"yaml","md5":"9f","content":"url: jdbc"}]}""".trimIndent(),
            ),
        )
        val result = client(transport).pollEffective(TestFixtures.identity(), null, 30000)
        val changed = assertIs<PollResult.Changed>(result)
        assertEquals("abc", changed.effective.md5)
        assertEquals(1, changed.effective.items.size)
        assertEquals("mysql.yml", changed.effective.items[0].dataId)
        assertEquals("url: jdbc", changed.effective.items[0].content)
    }

    @Test
    fun `pollEffective 304 返回 NotModified`() {
        val transport = FakeHttpTransport().enqueue(HttpResponse(304, ""))
        assertIs<PollResult.NotModified>(
            client(transport).pollEffective(TestFixtures.identity(), "abc", 30000),
        )
    }

    @Test
    fun `pollEffective 404 返回 NotRegistered`() {
        val transport = FakeHttpTransport().enqueue(HttpResponse(404, """{"code":"NOT_REGISTERED"}"""))
        assertIs<PollResult.NotRegistered>(
            client(transport).pollEffective(TestFixtures.identity(), "abc", 30000),
        )
    }

    @Test
    fun `pollEffective 首拉 md5 空且 url 带查询参数`() {
        val transport = FakeHttpTransport().enqueue(HttpResponse(304, ""))
        client(transport).pollEffective(TestFixtures.identity(), null, 30000)
        val url = transport.captured.single().url
        assertTrue(url.contains("namespace=prod"))
        assertTrue(url.contains("serverId=lobby-1"))
        assertTrue(url.contains("md5="))
        assertTrue(url.contains("timeoutMs=30000"))
    }

    @Test
    fun `heartbeat 404 映射 NotRegistered`() {
        val transport = FakeHttpTransport().enqueue(HttpResponse(404, ""))
        assertIs<HeartbeatOutcome.NotRegistered>(client(transport).heartbeat(TestFixtures.identity()))
    }

    @Test
    fun `heartbeat 连接失败映射 Failed`() {
        // 空响应队列 → execute 抛异常 → exec 吞为 null → Failed。
        val transport = FakeHttpTransport()
        assertIs<HeartbeatOutcome.Failed>(client(transport).heartbeat(TestFixtures.identity()))
    }

    @Test
    fun `discover 解析 instances 列表`() {
        val transport = FakeHttpTransport().enqueue(
            HttpResponse(
                200,
                """{"instances":[{"serverId":"lobby-1","role":"bukkit","group":"area1","zone":"zoneA",
                   "address":"10.0.0.7:25565","version":"1.4.2","status":"online","playerCount":12,
                   "capacity":200,"weight":100}]}""".trimIndent(),
            ),
        )
        val list = client(transport).discover("prod", "area1", null, null)
        assertEquals(1, list.size)
        assertEquals("lobby-1", list[0]["serverId"])
    }

    @Test
    fun `discover tags 拼为 tag dot key 查询参数`() {
        val transport = FakeHttpTransport().enqueue(HttpResponse(200, """{"instances":[]}"""))
        client(transport).discover(
            namespace = "prod",
            group = null,
            zone = null,
            role = "bukkit",
            tags = linkedMapOf("region" to "cn-east", "tier" to "premium"),
        )
        val url = transport.captured.single().url
        assertTrue(url.contains("role=bukkit"), "应含 role 过滤")
        assertTrue(url.contains("tag.region=cn-east"), "应把 tag 拼为 tag.region=cn-east，实际 $url")
        assertTrue(url.contains("tag.tier=premium"), "应把 tag 拼为 tag.tier=premium，实际 $url")
    }

    @Test
    fun `discover 无 tags 时 url 不含 tag 参数`() {
        val transport = FakeHttpTransport().enqueue(HttpResponse(200, """{"instances":[]}"""))
        client(transport).discover("prod", null, null, null)
        assertTrue(!transport.captured.single().url.contains("tag."), "无 tag 不应拼 tag 参数")
    }
}
