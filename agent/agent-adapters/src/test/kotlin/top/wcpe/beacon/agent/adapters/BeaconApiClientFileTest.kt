package top.wcpe.beacon.agent.adapters

import top.wcpe.beacon.agent.adapters.testutil.FakeHttpTransport
import top.wcpe.beacon.agent.adapters.testutil.TestFixtures
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.FileManifestPollResult
import top.wcpe.beacon.agent.core.transport.HttpResponse
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertIs
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * BeaconApiClient 文件树（通道B）调用对假 transport + 真实 KotlinxJsonCodec 的集成单测：
 * pollFileManifest 对 200/304/404 的映射、fetchFileContent 解析、请求 URL 与头。
 */
class BeaconApiClientFileTest {

    private val codec = KotlinxJsonCodec()

    private fun client(transport: FakeHttpTransport) =
        BeaconApiClient(transport, codec, TestFixtures.settings())

    @Test
    fun `pollFileManifest 200 返回 Changed 且解析 files 与 fileTreeMd5`() {
        val transport = FakeHttpTransport().enqueue(
            HttpResponse(
                200,
                """{"namespace":"prod","serverId":"lobby-1","group":"area1","zone":"zoneA","fileTreeMd5":"c4",
                   "files":[{"path":"ui-components/main.allin","md5":"9f"},{"path":"scripts/hello.js","md5":"77"}]}""".trimIndent(),
            ),
        )
        val result = client(transport).pollFileManifest(TestFixtures.identity(), null, 30000)
        val changed = assertIs<FileManifestPollResult.Changed>(result)
        assertEquals("c4", changed.manifest.fileTreeMd5)
        assertEquals(2, changed.manifest.entries.size)
        assertEquals("ui-components/main.allin", changed.manifest.entries[0].path)
        assertEquals("9f", changed.manifest.entries[0].md5)
    }

    @Test
    fun `pollFileManifest 304 返回 NotModified`() {
        val transport = FakeHttpTransport().enqueue(HttpResponse(304, ""))
        assertIs<FileManifestPollResult.NotModified>(
            client(transport).pollFileManifest(TestFixtures.identity(), "c4", 30000),
        )
    }

    @Test
    fun `pollFileManifest 404 返回 NotRegistered`() {
        val transport = FakeHttpTransport().enqueue(HttpResponse(404, """{"code":"NOT_REGISTERED"}"""))
        assertIs<FileManifestPollResult.NotRegistered>(
            client(transport).pollFileManifest(TestFixtures.identity(), "c4", 30000),
        )
    }

    @Test
    fun `pollFileManifest 首拉 md5 空且 url 带查询参数与 token 头`() {
        val transport = FakeHttpTransport().enqueue(HttpResponse(304, ""))
        client(transport).pollFileManifest(TestFixtures.identity(), null, 30000)
        val req = transport.captured.single()
        assertTrue(req.url.contains("/files/manifest"))
        assertTrue(req.url.contains("namespace=prod"))
        assertTrue(req.url.contains("serverId=lobby-1"))
        assertTrue(req.url.contains("md5="))
        assertTrue(req.url.contains("timeoutMs=30000"))
        assertEquals("test-token", req.headers["X-Beacon-Token"])
    }

    @Test
    fun `fetchFileContent 200 解析整文件内容`() {
        val transport = FakeHttpTransport().enqueue(
            HttpResponse(200, """{"path":"ui-components/main.allin","md5":"9f","content":"line1\nline2\n"}"""),
        )
        val content = client(transport).fetchFileContent(TestFixtures.identity(), "ui-components/main.allin")!!
        assertEquals("ui-components/main.allin", content.path)
        assertEquals("9f", content.md5)
        assertEquals("line1\nline2\n", content.content)
    }

    @Test
    fun `fetchFileContent 404 返回 null`() {
        val transport = FakeHttpTransport().enqueue(HttpResponse(404, """{"code":"FILE_NOT_FOUND"}"""))
        assertNull(client(transport).fetchFileContent(TestFixtures.identity(), "missing.yml"))
    }

    @Test
    fun `fetchFileContent url 带 path 查询参数`() {
        val transport = FakeHttpTransport().enqueue(HttpResponse(200, """{"path":"a.yml","md5":"1","content":"x"}"""))
        client(transport).fetchFileContent(TestFixtures.identity(), "dir/a.yml")
        val url = transport.captured.single().url
        assertTrue(url.contains("/files/content"))
        // path 经 URL 编码，斜杠编码为 %2F。
        assertTrue(url.contains("path=dir%2Fa.yml"))
    }
}
