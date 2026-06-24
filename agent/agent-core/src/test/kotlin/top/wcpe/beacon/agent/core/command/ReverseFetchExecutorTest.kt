package top.wcpe.beacon.agent.core.command

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.log.AgentLogBuffer
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
        val scanCalls = AtomicInteger(0)
        val lastScanBody = AtomicReference<String?>(null)
        val errorCalls = AtomicInteger(0)
        val lastErrorBody = AtomicReference<String?>(null)
        val logsCalls = AtomicInteger(0)
        val lastLogsBody = AtomicReference<String?>(null)
        val resultCalls = AtomicInteger(0)
        val lastResultBody = AtomicReference<String?>(null)

        override fun execute(request: HttpRequest): HttpResponse = when {
            // 命令结果回传端点（FR-91）：URL 含 /agent/commands/result（须在通用 /agent/commands 前判定）。
            request.url.contains("/agent/commands/result") -> {
                resultCalls.incrementAndGet()
                lastResultBody.set(request.body)
                HttpResponse(200, "")
            }

            request.url.contains("/agent/commands") -> {
                if (served.getAndIncrement() < pendingCount) HttpResponse(200, pendingBody) else HttpResponse(204, "")
            }

            // 取日志回传端点（FR-88）：URL 含 /agent/logs（须在通用分支前判定）。
            request.url.contains("/agent/logs") -> {
                logsCalls.incrementAndGet()
                lastLogsBody.set(request.body)
                HttpResponse(200, "")
            }

            // error 端点须在 scan/ingest 之前判定：URL 含 /agent/files/error（FR-87，不与 scan/ingest 混淆）。
            request.url.contains("/agent/files/error") -> {
                errorCalls.incrementAndGet()
                lastErrorBody.set(request.body)
                HttpResponse(200, "")
            }

            // scan 端点须在 ingest 之前判定：URL 含 /agent/files/scan（不与 /ingest 混淆）。
            request.url.contains("/agent/files/scan") -> {
                scanCalls.incrementAndGet()
                lastScanBody.set(request.body)
                HttpResponse(200, "")
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

            CMD_SCAN -> mapOf(
                "id" to 9,
                "type" to "ingest-plugins",
                "payload" to mapOf("scope" to "group", "group" to "area1", "target" to "", "mode" to "scan"),
            )

            CMD_SUBMIT -> mapOf(
                "id" to 10,
                "type" to "ingest-plugins",
                "payload" to mapOf(
                    "scope" to "group", "group" to "area1", "target" to "", "mode" to "submit",
                    "selectedPaths" to listOf("config.yml", "lang/zh.yml"),
                ),
            )

            CMD_TAIL_LOGS -> mapOf(
                "id" to 11,
                "type" to "tail-logs",
                "payload" to emptyMap<String, Any?>(),
            )

            CMD_RESYNC -> mapOf(
                "id" to 12,
                "type" to "resync-config",
                "payload" to emptyMap<String, Any?>(),
            )

            else -> emptyMap<String, Any?>()
        }
    }

    /**
     * 读盘可编排的平台桩：readPluginsTree / readPluginsTreeMetadata 返回预置数据并计数；同步执行 runAsync。
     * failRead=true 时两读盘方法均抛 IOException，模拟 plugins 目录读不了（FR-87 错误回传场景）。
     */
    private class StubAdapter(
        private val tree: Map<String, ByteArray>,
        private val failRead: Boolean = false,
    ) : PlatformAdapter {
        val readCalls = AtomicInteger(0)
        val metadataCalls = AtomicInteger(0)

        override fun runAsync(task: () -> Unit) = task()
        override fun runAsyncDelayed(delayMs: Long, task: () -> Unit) = task()
        override fun runSync(task: () -> Unit) = task()
        override fun dataFolder(): File = File(System.getProperty("java.io.tmpdir"))
        override fun readPluginsTree(): Map<String, ByteArray> {
            readCalls.incrementAndGet()
            if (failRead) throw java.io.IOException("permission denied")
            return tree
        }

        override fun readPluginsTreeMetadata(): Map<String, Long> {
            metadataCalls.incrementAndGet()
            if (failRead) throw java.io.IOException("permission denied")
            // 元信息桩：由预置树字节数派生大小（scan 只关心大小，不读内容）。
            return tree.mapValues { it.value.size.toLong() }
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

    /** 带日志缓冲的执行器（FR-88 取日志路径用）。 */
    private fun executor(transport: FakeTransport, adapter: StubAdapter, buffer: AgentLogBuffer): ReverseFetchExecutor {
        val client = BeaconApiClient(transport, FakeCodec(), settings())
        return ReverseFetchExecutor(identity(), client, adapter, buffer)
    }

    /** 带强制重同步回调的执行器（FR-91 重同步路径用；回调返回 true=已执行、false=因未运行跳过）。 */
    private fun executor(transport: FakeTransport, adapter: StubAdapter, onResync: () -> Boolean): ReverseFetchExecutor {
        val client = BeaconApiClient(transport, FakeCodec(), settings())
        return ReverseFetchExecutor(identity(), client, adapter, onResyncConfig = onResync)
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

    @Test
    fun `scan 模式只读元信息走 scan 端点不走 ingest`() {
        // 含超 1MB 文件的树：scan 不应失败、列出全部（治根）。
        val big = ByteArray((PluginIngestLimits.MAX_FILE_BYTES + 1).toInt()) { 'a'.code.toByte() }
        val transport = FakeTransport(pendingBody = CMD_SCAN)
        val adapter = StubAdapter(
            mapOf(
                "config.yml" to b("k: v"),
                "metrics.jsonl" to big, // 超阈值
                "plugin.jar" to b("MZ"), // 剔除
            ),
        )
        executor(transport, adapter).trigger()

        assertEquals(1, adapter.metadataCalls.get(), "scan 应只 stat 元信息一次")
        assertEquals(0, adapter.readCalls.get(), "scan 绝不读内容")
        assertEquals(1, transport.scanCalls.get(), "应走 /agent/files/scan 一次")
        assertEquals(0, transport.ingestCalls.get(), "scan 不走 ingest")
        val body = transport.lastScanBody.get()!!
        assertTrue(body.contains("commandId=9"), "scan 报文应携命令 id：$body")
        assertTrue(body.contains("config.yml") && body.contains("metrics.jsonl"), "超阈值文件应列出（不失败）：$body")
        assertTrue(body.contains("overThreshold=true"), "超阈值应红标：$body")
        assertTrue(!body.contains("plugin.jar"), "jar 不应进清单")
    }

    @Test
    fun `submit 模式只回传选定子集`() {
        val transport = FakeTransport(pendingBody = CMD_SUBMIT) // selectedPaths = [config.yml, lang/zh.yml]
        val adapter = StubAdapter(
            mapOf(
                "config.yml" to b("k: v"),
                "lang/zh.yml" to b("hi: 你好"),
                "secret.yml" to b("token: x"), // 未选定 → 不回传
            ),
        )
        executor(transport, adapter).trigger()

        assertEquals(1, adapter.readCalls.get(), "submit 应读内容一次")
        assertEquals(1, transport.ingestCalls.get(), "submit 走 ingest 一次")
        assertEquals(0, transport.scanCalls.get(), "submit 不走 scan")
        val body = transport.lastIngestBody.get()!!
        assertTrue(body.contains("config.yml") && body.contains("lang/zh.yml"), "选定文件应回传：$body")
        assertTrue(!body.contains("secret.yml"), "未选定不应回传：$body")
    }

    @Test
    fun `scan 读盘失败回传错误到 error 端点`() {
        // FR-87：scan 读元信息失败 → 回传 /agent/files/error，令控制面任务转 failed 记 lastError（不静默卡 scanning）。
        val transport = FakeTransport(pendingBody = CMD_SCAN)
        val adapter = StubAdapter(emptyMap(), failRead = true)
        executor(transport, adapter).trigger()

        assertEquals(1, adapter.metadataCalls.get(), "scan 应尝试读元信息一次")
        assertEquals(0, transport.scanCalls.get(), "读失败不应回传清单")
        assertEquals(0, transport.ingestCalls.get(), "scan 不走 ingest")
        assertEquals(1, transport.errorCalls.get(), "读失败应回传错误一次")
        val body = transport.lastErrorBody.get()!!
        assertTrue(body.contains("commandId=9"), "错误回传应携命令 id：$body")
        assertTrue(body.contains("reason="), "错误回传应携 reason：$body")
    }

    @Test
    fun `submit 读盘失败回传错误到 error 端点`() {
        // FR-87：submit 读内容失败 → 回传 /agent/files/error（其所属任务处 fetching）。
        val transport = FakeTransport(pendingBody = CMD_SUBMIT)
        val adapter = StubAdapter(emptyMap(), failRead = true)
        executor(transport, adapter).trigger()

        assertEquals(1, adapter.readCalls.get(), "submit 应尝试读内容一次")
        assertEquals(0, transport.ingestCalls.get(), "读失败不应回传内容")
        assertEquals(1, transport.errorCalls.get(), "读失败应回传错误一次")
        val body = transport.lastErrorBody.get()!!
        assertTrue(body.contains("commandId=10"), "错误回传应携命令 id：$body")
    }

    @Test
    fun `旧整树读盘失败不回传错误`() {
        // 向后兼容：旧整树命令（无 mode、非受管任务）读盘失败不回传错误（无对应任务，沿旧静默放弃语义）。
        val transport = FakeTransport(pendingBody = CMD_INGEST)
        val adapter = StubAdapter(emptyMap(), failRead = true)
        executor(transport, adapter).trigger()

        assertEquals(0, transport.errorCalls.get(), "旧整树读盘失败不应回传错误")
        assertEquals(0, transport.ingestCalls.get(), "读失败不应回传内容")
    }

    @Test
    fun `取日志命令读缓冲快照回传到 logs 端点不读盘`() {
        // FR-88：tail-logs 命令 → 读自身日志环形缓冲快照回传，绝不读 plugins 树。
        val transport = FakeTransport(pendingBody = CMD_TAIL_LOGS)
        val adapter = StubAdapter(mapOf("config.yml" to b("k: v")))
        val buffer = AgentLogBuffer(capacity = 50)
        buffer.append("INFO", "已应用有效配置 md5=abc")
        buffer.append("WARN", "心跳一次失败")
        executor(transport, adapter, buffer).trigger()

        assertEquals(0, adapter.readCalls.get(), "取日志绝不读 plugins 内容")
        assertEquals(0, adapter.metadataCalls.get(), "取日志绝不 stat plugins 元信息")
        assertEquals(0, transport.ingestCalls.get(), "取日志不走 ingest")
        assertEquals(1, transport.logsCalls.get(), "应走 /agent/logs 一次")
        val body = transport.lastLogsBody.get()!!
        assertTrue(body.contains("commandId=11"), "日志回传应携命令 id：$body")
        assertTrue(body.contains("已应用有效配置") && body.contains("心跳一次失败"), "应回传缓冲行：$body")
    }

    @Test
    fun `未注入日志缓冲时取日志命令忽略不回传`() {
        // 向后兼容：未启用日志缓冲（executor 无 buffer）→ tail-logs 按未知能力忽略、不回传。
        val transport = FakeTransport(pendingBody = CMD_TAIL_LOGS)
        val adapter = StubAdapter(mapOf("config.yml" to b("k: v")))
        executor(transport, adapter).trigger() // 无 buffer 重载

        assertEquals(0, transport.logsCalls.get(), "未启用缓冲不应回传日志")
        assertEquals(0, adapter.readCalls.get(), "不应读盘")
    }

    @Test
    fun `重同步命令调回调并回传 done 不读盘`() {
        // FR-91：resync-config 命令 → 调重同步回调 → 经命令结果端点回传 done，绝不读 plugins 树。
        val transport = FakeTransport(pendingBody = CMD_RESYNC)
        val adapter = StubAdapter(mapOf("config.yml" to b("k: v")))
        val resyncCalls = AtomicInteger(0)
        executor(transport, adapter) { resyncCalls.incrementAndGet(); true }.trigger()

        assertEquals(1, resyncCalls.get(), "应调重同步回调一次")
        assertEquals(0, adapter.readCalls.get(), "重同步绝不读 plugins 内容")
        assertEquals(0, adapter.metadataCalls.get(), "重同步绝不 stat plugins 元信息")
        assertEquals(0, transport.ingestCalls.get(), "重同步不走 ingest")
        assertEquals(1, transport.resultCalls.get(), "应走 /agent/commands/result 一次")
        val body = transport.lastResultBody.get()!!
        assertTrue(body.contains("commandId=12"), "结果回传应携命令 id：$body")
        assertTrue(body.contains("ok=true"), "成功应回传 ok=true：$body")
    }

    @Test
    fun `重同步回调抛异常回传 failed`() {
        // FR-91：回调抛异常 → 回传 ok=false（控制面据此 CAS failed）。
        val transport = FakeTransport(pendingBody = CMD_RESYNC)
        val adapter = StubAdapter(emptyMap())
        executor(transport, adapter) { throw IllegalStateException("boom") }.trigger()

        assertEquals(1, transport.resultCalls.get(), "失败也应回传结果一次")
        val body = transport.lastResultBody.get()!!
        assertTrue(body.contains("commandId=12"), "结果回传应携命令 id：$body")
        assertTrue(body.contains("ok=false"), "失败应回传 ok=false：$body")
    }

    @Test
    fun `重同步回调返回 false（未运行跳过）回传 failed`() {
        // FR-91 followup：停机窗口（forceResyncNow 因 !running 返回 false / lifecycle 持有者为 null）→
        // 回调返回 false，runResync 必须回传 ok=false，不误报 done（真实未执行重拉）。
        val transport = FakeTransport(pendingBody = CMD_RESYNC)
        val adapter = StubAdapter(mapOf("config.yml" to b("k: v")))
        executor(transport, adapter) { false }.trigger() // 模拟未运行/停机：回调返回 false

        assertEquals(0, adapter.readCalls.get(), "重同步绝不读 plugins 内容")
        assertEquals(1, transport.resultCalls.get(), "跳过也应回传结果一次")
        val body = transport.lastResultBody.get()!!
        assertTrue(body.contains("commandId=12"), "结果回传应携命令 id：$body")
        assertTrue(body.contains("ok=false"), "跳过应回传 ok=false（不误报 done）：$body")
    }

    @Test
    fun `未注入重同步回调时重同步命令忽略不回传`() {
        // 向后兼容：未注入回调（executor 无 onResyncConfig）→ resync-config 按未知能力忽略、不回传。
        val transport = FakeTransport(pendingBody = CMD_RESYNC)
        val adapter = StubAdapter(mapOf("config.yml" to b("k: v")))
        executor(transport, adapter).trigger() // 无回调重载

        assertEquals(0, transport.resultCalls.get(), "未注入回调不应回传结果")
        assertEquals(0, adapter.readCalls.get(), "不应读盘")
    }

    companion object {
        private const val CMD_INGEST = "cmd-ingest"
        private const val CMD_UNKNOWN = "cmd-unknown"
        private const val CMD_SCAN = "cmd-scan"
        private const val CMD_SUBMIT = "cmd-submit"
        private const val CMD_TAIL_LOGS = "cmd-tail-logs"
        private const val CMD_RESYNC = "cmd-resync"
    }
}
