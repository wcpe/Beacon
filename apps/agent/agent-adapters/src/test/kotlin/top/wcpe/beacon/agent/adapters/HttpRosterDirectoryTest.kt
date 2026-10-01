package top.wcpe.beacon.agent.adapters

import top.wcpe.beacon.agent.adapters.testutil.FakeHttpTransport
import top.wcpe.beacon.agent.adapters.testutil.TestFixtures
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.messaging.RosterDirectoryHolder
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import java.io.IOException
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * HttpRosterDirectory 单测（FR-31 / ADR-0063 决策 4）：
 * 控制面 GET /beacon/v2/agent/player-roster 的取数装配与降级行为。
 *
 * 锁定：
 * - 请求形状：GET + 固定路径、v2 鉴权头（Token / Identity / Boot）、**不带任何查询参数**（namespace 隔离由服务端按身份保证）；
 * - 200 的 players 映射原样进 Map<String, String>；
 * - 401（未注册）/ 5xx / 连接级异常 / 响应体非法 → 空 Map 且不抛（守端口「绝不抛」契约）；
 * - 注入 [RosterDirectoryHolder] 后 snapshot 即控制面名册（壳层装配路径）。
 */
class HttpRosterDirectoryTest {
    private val codec = KotlinxJsonCodec()

    /** 已绑定 v2 身份：名册端点按 X-Beacon-Identity / X-Beacon-Boot 圈定调用方所属 namespace。 */
    private val identity =
        TestFixtures.identity().copy(
            identityId = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
            bootId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
        )

    private fun directory(transport: HttpTransport): HttpRosterDirectory =
        HttpRosterDirectory(BeaconApiClient(transport, codec, TestFixtures.settings()), identity)

    /** 连接级失败 transport：照 OkHttp 真实失败路径抛 IOException（由 BeaconApiClient.exec 收成空结果）。 */
    private class ThrowingTransport(private val error: IOException) : HttpTransport {
        override fun execute(request: HttpRequest): HttpResponse = throw error
    }

    @Test
    fun `200 返回 players 映射且请求带 v2 鉴权头无查询参数`() {
        val transport =
            FakeHttpTransport().enqueue(
                HttpResponse(200, """{"namespace":"prod","count":2,"players":{"Alice":"lobby-1","Bob":"game-1"}}"""),
            )

        val roster = directory(transport).snapshot()

        assertEquals(mapOf("Alice" to "lobby-1", "Bob" to "game-1"), roster, "players 应原样映射为名册全表")
        val req = transport.captured.single()
        assertEquals("GET", req.method)
        assertTrue(req.url.endsWith("/beacon/v2/agent/player-roster"), "请求路径应为 v2 名册端点，实际：${req.url}")
        assertTrue("?" !in req.url, "不得携带查询参数（namespace 隔离由服务端按身份保证），实际：${req.url}")
        assertEquals("test-token", req.headers["X-Beacon-Token"])
        assertEquals("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", req.headers["X-Beacon-Identity"])
        assertEquals("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", req.headers["X-Beacon-Boot"])
    }

    @Test
    fun `200 本域无人在线时返空 Map`() {
        // 控制面「暂时无人」也走 200 + 空 players（不是 404）——名册为空，不是端点不可用。
        val transport = FakeHttpTransport().enqueue(HttpResponse(200, """{"namespace":"prod","count":0,"players":{}}"""))

        assertTrue(directory(transport).snapshot().isEmpty(), "本域无人在线应返空 Map")
    }

    @Test
    fun `401 未注册返空 Map 不抛`() {
        // 未注册 / 身份失效（如身份被撤销后注册前预注入）时机下控制面回 401：降级返空，不抛。
        val transport = FakeHttpTransport().enqueue(HttpResponse(401, """{"code":"UNAUTHORIZED"}"""))

        assertTrue(directory(transport).snapshot().isEmpty(), "401 应降级返空 Map")
    }

    @Test
    fun `5xx 返空 Map 不抛`() {
        val transport = FakeHttpTransport().enqueue(HttpResponse(503, """{"message":"名册暂不可用"}"""))

        assertTrue(directory(transport).snapshot().isEmpty(), "5xx 应降级返空 Map")
    }

    @Test
    fun `连接级异常返空 Map 不抛`() {
        val transport = ThrowingTransport(IOException("connection refused to localhost:8848"))

        assertTrue(directory(transport).snapshot().isEmpty(), "连接异常应降级返空 Map、绝不外抛")
    }

    @Test
    fun `响应体非法返空 Map 不抛`() {
        // 场景一：响应体不是合法 JSON；场景二：players 不是对象。
        val brokenJson = directory(FakeHttpTransport().enqueue(HttpResponse(200, "<html>502 Bad Gateway</html>")))
        val brokenShape = directory(FakeHttpTransport().enqueue(HttpResponse(200, """{"namespace":"prod","count":0,"players":[]}""")))

        assertTrue(brokenJson.snapshot().isEmpty(), "响应体非法 JSON 应降级返空 Map")
        assertTrue(brokenShape.snapshot().isEmpty(), "players 非对象应降级返空 Map")
    }

    @Test
    fun `条目值非字符串时丢该条保留其余条目`() {
        val transport =
            FakeHttpTransport().enqueue(
                HttpResponse(200, """{"namespace":"prod","count":2,"players":{"Alice":"lobby-1","Bob":123}}"""),
            )

        assertEquals(
            mapOf("Alice" to "lobby-1"),
            directory(transport).snapshot(),
            "单条脏数据只丢该条，不该让整份名册不可用",
        )
    }

    @Test
    fun `注入 RosterDirectoryHolder 后 snapshot 即控制面名册`() {
        // 壳层装配路径：装配期建 holder（默认降级）→ 注册成功后 set(适配器) → 门面读到的即控制面名册。
        val holder = RosterDirectoryHolder()
        assertTrue(holder.snapshot().isEmpty(), "未注入实现时应为降级空名册")

        holder.set(directory(FakeHttpTransport().enqueue(HttpResponse(200, """{"players":{"Alice":"lobby-1"}}"""))))
        assertEquals(mapOf("Alice" to "lobby-1"), holder.snapshot(), "注入适配器后 holder 应透出控制面名册")

        holder.reset()
        assertTrue(holder.snapshot().isEmpty(), "停止后复位应回到降级空名册")
    }
}
