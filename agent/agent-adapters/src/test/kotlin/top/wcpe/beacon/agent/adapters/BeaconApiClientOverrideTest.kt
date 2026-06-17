package top.wcpe.beacon.agent.adapters

import top.wcpe.beacon.agent.adapters.testutil.FakeHttpTransport
import top.wcpe.beacon.agent.adapters.testutil.TestFixtures
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.OverridePollResult
import top.wcpe.beacon.agent.core.transport.HttpResponse
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertIs
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * BeaconApiClient 三方覆盖集投递（FR-15）对假 transport + 真实 KotlinxJsonCodec 的集成单测：
 * pollOverrideSets 对 200/304/404 的映射与解析、fetchOverrideMember 解析、空命令归一化为 null。
 * 验证 agent↔控制面 投递端点的 JSON 契约。
 */
class BeaconApiClientOverrideTest {

    private val codec = KotlinxJsonCodec()

    private fun client(transport: FakeHttpTransport) =
        BeaconApiClient(transport, codec, TestFixtures.settings())

    @Test
    fun `pollOverrideSets 200 解析 sets 与 overrideMd5`() {
        val transport = FakeHttpTransport().enqueue(
            HttpResponse(
                200,
                """{"namespace":"prod","serverId":"lobby-1","overrideMd5":"ab",
                   "sets":[{"name":"AllinCore","targetRoot":"plugins/AllinCore","reloadCommand":"allin reload",
                            "members":["config.yml","scripts/hello.js"]}]}""".trimIndent(),
            ),
        )
        val result = client(transport).pollOverrideSets(TestFixtures.identity(), null, 30000)
        val changed = assertIs<OverridePollResult.Changed>(result)
        assertEquals("ab", changed.manifest.overrideMd5)
        assertEquals(1, changed.manifest.sets.size)
        val set = changed.manifest.sets[0]
        assertEquals("AllinCore", set.name)
        assertEquals("plugins/AllinCore", set.targetRoot)
        assertEquals("allin reload", set.reloadCommand)
        assertEquals(listOf("config.yml", "scripts/hello.js"), set.members)
    }

    @Test
    fun `pollOverrideSets 空命令归一化为 null`() {
        val transport = FakeHttpTransport().enqueue(
            HttpResponse(
                200,
                """{"namespace":"prod","serverId":"lobby-1","overrideMd5":"ab",
                   "sets":[{"name":"AllinCore","targetRoot":"plugins/AllinCore","reloadCommand":"","members":["config.yml"]}]}""".trimIndent(),
            ),
        )
        val result = client(transport).pollOverrideSets(TestFixtures.identity(), null, 30000)
        val changed = assertIs<OverridePollResult.Changed>(result)
        assertNull(changed.manifest.sets[0].reloadCommand, "空命令应归一化为 null（不下发命令）")
    }

    @Test
    fun `pollOverrideSets 304 返回 NotModified`() {
        val transport = FakeHttpTransport().enqueue(HttpResponse(304, ""))
        assertIs<OverridePollResult.NotModified>(
            client(transport).pollOverrideSets(TestFixtures.identity(), "ab", 30000),
        )
    }

    @Test
    fun `pollOverrideSets 404 返回 NotRegistered`() {
        val transport = FakeHttpTransport().enqueue(HttpResponse(404, """{"code":"NOT_REGISTERED"}"""))
        assertIs<OverridePollResult.NotRegistered>(
            client(transport).pollOverrideSets(TestFixtures.identity(), "ab", 30000),
        )
    }

    @Test
    fun `pollOverrideSets url 带查询参数与 token 头`() {
        val transport = FakeHttpTransport().enqueue(HttpResponse(304, ""))
        client(transport).pollOverrideSets(TestFixtures.identity(), null, 30000)
        val req = transport.captured.single()
        assertTrue(req.url.contains("/override-sets"))
        assertTrue(req.url.contains("namespace=prod"))
        assertTrue(req.url.contains("serverId=lobby-1"))
        assertTrue(req.url.contains("timeoutMs=30000"))
        assertEquals("test-token", req.headers["X-Beacon-Token"])
    }

    @Test
    fun `fetchOverrideMember 200 解析整文件内容`() {
        val transport = FakeHttpTransport().enqueue(
            HttpResponse(200, """{"set":"AllinCore","path":"config.yml","md5":"9f","content":"k: 1\n"}"""),
        )
        val content = client(transport).fetchOverrideMember(TestFixtures.identity(), "AllinCore", "config.yml")!!
        assertEquals("config.yml", content.path)
        assertEquals("9f", content.md5)
        assertEquals("k: 1\n", content.content)
    }

    @Test
    fun `fetchOverrideMember 404 返回 null`() {
        val transport = FakeHttpTransport().enqueue(HttpResponse(404, """{"code":"FILE_NOT_FOUND"}"""))
        assertNull(client(transport).fetchOverrideMember(TestFixtures.identity(), "AllinCore", "missing.yml"))
    }

    @Test
    fun `fetchOverrideMember url 带 set 与 path 查询参数`() {
        val transport = FakeHttpTransport().enqueue(HttpResponse(200, """{"set":"AllinCore","path":"a.yml","md5":"1","content":"x"}"""))
        client(transport).fetchOverrideMember(TestFixtures.identity(), "AllinCore", "dir/a.yml")
        val url = transport.captured.single().url
        assertTrue(url.contains("/override-sets/content"))
        assertTrue(url.contains("set=AllinCore"))
        // path 经 URL 编码，斜杠编码为 %2F。
        assertTrue(url.contains("path=dir%2Fa.yml"))
    }
}
