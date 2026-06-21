package top.wcpe.beacon.agent.core.command

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.BackoffSettings
import top.wcpe.beacon.agent.core.settings.FileTreeSettings
import top.wcpe.beacon.agent.core.settings.OverrideSettings
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.io.File
import java.nio.charset.StandardCharsets
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicReference
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * 反向抓取执行器 [ReverseFetchExecutor] 编排单测（FR-39，见 ADR-0027）：
 * - 无待办命令 → 不读盘、不上传；
 * - 有命令 + 文本树 → 过滤后 uploadIngest，报文携命令 id + 文本文件；
 * - 超限（单文件超 1MB）→ 整体失败、不上传（不部分上传）；
 * - 未知命令类型 → 不读盘、不上传；
 * - 读盘剔除 jar / 二进制（端到端经 PluginsTreeFilter）。
 */
class ReverseFetchExecutorTest {

    /**
     * 按 URL 路由的 transport：commands 端点把预置命令体**只发 pendingCount 次**（模拟控制面 CAS：一条 pending 被拉走即 fetched），
     * 之后返回 204（无待办）——配合执行器的排空循环，避免同一命令被无限重拉。ingest 端点记录命中并返回 200。
     */
    private class FakeTransport(private val pendingCount: Int = 1, private val pendingBody: String = CMD_INGEST) : HttpTransport {
        private val served = AtomicInteger(0)

        val ingestCalls = AtomicInteger(0)
        val lastIngestBody = AtomicReference<String?>(null)

        override fun execute(request: HttpRequest): HttpResponse = when {
            request.url.contains("/agent/commands") -> {
                if (served.getAndIncrement() < pendingCount) HttpResponse(200, pendingBody) else HttpResponse(204, "")
            }

            request.url.contains("/agent/files/ingest") -> {
                ingestCalls.incrementAndGet()
                lastIngestBody.set(request.body)
                HttpResponse(200, "")
            }

            else -> HttpResponse(404, "")
        }
    }

    /** 极简 codec：decode 按 body key 给命令树；encode 把上行 Map 透传为可断言的字符串。 */
    private class FakeCodec : JsonCodec {
        override fun encode(value: Any?): String = value.toString()

        override fun decode(json: String): Any? = when (json) {
            CMD_INGEST -> mapOf(
                "id" to 7,
                "type" to "ingest-plugins",
                "payload" to mapOf("scope" to "group", "group" to "area1", "target" to ""),
            )

            CMD_UNKNOWN -> mapOf(
                "id" to 8,
                "type" to "some-future-command",
                "payload" to mapOf("scope" to "group", "group" to "area1", "target" to ""),
            )

            else -> emptyMap<String, Any?>()
        }
    }

    /** 读盘可编排的平台桩：readPluginsTree 返回预置树并计数；同步执行 runAsync。 */
    private class StubAdapter(private val tree: Map<String, ByteArray>) : PlatformAdapter {
        val readCalls = AtomicInteger(0)

        override fun runAsync(task: () -> Unit) = task()
        override fun runAsyncDelayed(delayMs: Long, task: () -> Unit) = task()
        override fun runSync(task: () -> Unit) = task()
        override fun dataFolder(): File = File(System.getProperty("java.io.tmpdir"))
        override fun readPluginsTree(): Map<String, ByteArray> {
            readCalls.incrementAndGet()
            return tree
        }

        override fun publishConfigChanged(changed: Set<String>, newMd5: String) {}
        override fun info(msg: String) {}
        override fun warn(msg: String) {}
        override fun error(msg: String, t: Throwable?) {}
    }

    private fun b(s: String): ByteArray = s.toByteArray(StandardCharsets.UTF_8)

    private fun identity() = AgentIdentity(
        namespace = "prod", serverId = "lobby-1", role = "bukkit", groupHint = "area1",
        address = "127.0.0.1:25565", version = "1.0", capacity = 100, weight = 1, metadata = emptyMap(),
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
        fileTree = FileTreeSettings(enabled = false, targetSubDir = "", appliedManifestFileName = "x.json"),
        override = OverrideSettings(commandWhitelist = emptySet(), backupDirName = "bk"),
    )

    private fun executor(transport: FakeTransport, adapter: StubAdapter): ReverseFetchExecutor {
        val client = BeaconApiClient(transport, FakeCodec(), settings())
        return ReverseFetchExecutor(identity(), client, adapter)
    }

    @Test
    fun `无待办命令时不读盘不上传`() {
        val transport = FakeTransport(pendingCount = 0) // 始终 204 无待办
        val adapter = StubAdapter(mapOf("config.yml" to b("k: v")))
        executor(transport, adapter).trigger()

        assertEquals(0, adapter.readCalls.get(), "无命令不应读盘")
        assertEquals(0, transport.ingestCalls.get(), "无命令不应上传")
    }

    @Test
    fun `有命令与文本树时过滤后上传`() {
        val transport = FakeTransport() // 默认发 1 条 CMD_INGEST 后 204
        val adapter = StubAdapter(
            mapOf(
                "config.yml" to b("k: v"),
                "plugin.jar" to b("MZ"), // 应剔除
                "world.dat" to byteArrayOf(0x00, 0x01), // 二进制应剔除
                "lang/zh.yml" to b("hi: 你好"),
            ),
        )
        executor(transport, adapter).trigger()

        assertEquals(1, adapter.readCalls.get(), "有命令应读盘一次")
        assertEquals(1, transport.ingestCalls.get(), "应上传一次")
        val body = transport.lastIngestBody.get()!!
        // 报文含命令 id 与文本文件，且不含 jar / 二进制 path。
        assertTrue(body.contains("commandId=7"), "报文应携命令 id：$body")
        assertTrue(body.contains("config.yml"))
        assertTrue(body.contains("lang/zh.yml"))
        assertTrue(!body.contains("plugin.jar"), "jar 不应进回传")
        assertTrue(!body.contains("world.dat"), "二进制不应进回传")
    }

    @Test
    fun `超限整体失败不上传`() {
        val big = ByteArray((PluginIngestLimits.MAX_FILE_BYTES + 1).toInt()) { 'a'.code.toByte() }
        val transport = FakeTransport() // 默认发 1 条 CMD_INGEST 后 204
        val adapter = StubAdapter(mapOf("huge.yml" to big, "small.yml" to b("k: v")))
        executor(transport, adapter).trigger()

        assertEquals(1, adapter.readCalls.get(), "应读盘")
        assertEquals(0, transport.ingestCalls.get(), "超限应整体失败、不部分上传")
    }

    @Test
    fun `未知命令类型不读盘不上传`() {
        val transport = FakeTransport(pendingBody = CMD_UNKNOWN) // 发 1 条未知类型后 204
        val adapter = StubAdapter(mapOf("config.yml" to b("k: v")))
        executor(transport, adapter).trigger()

        assertEquals(0, adapter.readCalls.get(), "未知命令类型不应读盘")
        assertEquals(0, transport.ingestCalls.get(), "未知命令类型不应上传")
    }

    @Test
    fun `空文本树上传空文件集`() {
        // 树全是排除项 → 过滤后空集，仍上传（让控制面据回执推进命令；空 ingest 合法）。
        val transport = FakeTransport() // 默认发 1 条 CMD_INGEST 后 204
        val adapter = StubAdapter(mapOf("plugin.jar" to b("MZ"), "bin.dat" to byteArrayOf(0x00)))
        executor(transport, adapter).trigger()

        assertEquals(1, transport.ingestCalls.get(), "过滤后空集仍上传一次")
        // 上行报文 files 为空数组（path 列表不含任何被剔除项）。
        val body = transport.lastIngestBody.get()!!
        assertTrue(!body.contains("plugin.jar") && !body.contains("bin.dat"), "剔除项不应进回传：$body")
    }

    companion object {
        private const val CMD_INGEST = "cmd-ingest"
        private const val CMD_UNKNOWN = "cmd-unknown"
    }
}
