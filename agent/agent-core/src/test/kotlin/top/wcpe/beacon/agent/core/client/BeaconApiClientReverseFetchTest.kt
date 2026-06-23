package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.command.AgentCommand
import top.wcpe.beacon.agent.core.command.IngestFile
import top.wcpe.beacon.agent.core.command.ScanFile
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
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * BeaconApiClient 反向抓取命令通道契约单测（FR-39，见 ADR-0027）：
 * - fetchPendingCommand：200 解析命令 / 204 无待办返回 null / 其它返回 null；URL + token 头正确；
 * - uploadIngest：报文为 {commandId, files:[{path,content}]}；200→true，其它→false；命中 /agent/files/ingest。
 */
class BeaconApiClientReverseFetchTest {

    /** 按 URL 路由的可编排 transport：记录最近请求，按预置状态码 / 体作答。 */
    private class ScriptTransport : HttpTransport {
        val lastRequest = AtomicReference<HttpRequest?>(null)

        @Volatile
        var commandsStatus: Int = 204

        @Volatile
        var commandsBody: String = ""

        @Volatile
        var ingestStatus: Int = 200

        @Volatile
        var scanStatus: Int = 200

        override fun execute(request: HttpRequest): HttpResponse {
            lastRequest.set(request)
            return when {
                request.url.contains("/agent/commands") -> HttpResponse(commandsStatus, commandsBody)
                // scan 端点须在 ingest 之前判定（URL 含 /scan，不与 /ingest 混淆）。
                request.url.contains("/agent/files/scan") -> HttpResponse(scanStatus, "")
                request.url.contains("/agent/files/ingest") -> HttpResponse(ingestStatus, "")
                else -> HttpResponse(404, "")
            }
        }
    }

    /** decode 把 body 当作命令 JSON 树返回（key 即预置树）；encode 捕获上行报文供断言。 */
    private class CmdCodec : JsonCodec {
        val lastEncoded = AtomicReference<Any?>(null)

        override fun encode(value: Any?): String {
            lastEncoded.set(value)
            return "encoded"
        }

        override fun decode(json: String): Any? = when (json) {
            BODY_INGEST_CMD -> mapOf(
                "id" to 42,
                "type" to "ingest-plugins",
                "payload" to mapOf("scope" to "group", "group" to "area1", "target" to ""),
            )

            BODY_SCAN_CMD -> mapOf(
                "id" to 43,
                "type" to "ingest-plugins",
                "payload" to mapOf("scope" to "group", "group" to "area1", "target" to "", "mode" to "scan"),
            )

            BODY_SUBMIT_CMD -> mapOf(
                "id" to 44,
                "type" to "ingest-plugins",
                "payload" to mapOf(
                    "scope" to "server", "group" to "area1", "target" to "lobby-1", "mode" to "submit",
                    "selectedPaths" to listOf("config.yml", "lang/zh.yml"),
                ),
            )

            else -> emptyMap<String, Any?>()
        }
    }

    private fun identity() = AgentIdentity(
        namespace = "prod",
        serverId = "lobby-1",
        role = "bukkit",
        groupHint = "area1",
        address = "127.0.0.1:25565",
        version = "1.0",
        capacity = 100,
        weight = 1,
        metadata = emptyMap(),
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

    @Test
    fun `fetchPendingCommand 解析 200 命令`() {
        val transport = ScriptTransport().apply {
            commandsStatus = 200
            commandsBody = BODY_INGEST_CMD
        }
        val client = BeaconApiClient(transport, CmdCodec(), settings())

        val cmd = client.fetchPendingCommand(identity())
        assertEquals(42L, cmd?.id)
        assertEquals(AgentCommand.TYPE_INGEST_PLUGINS, cmd?.type)
        assertEquals("group", cmd?.payload?.scope)
        assertEquals("area1", cmd?.payload?.group)
        // URL 携带 namespace + serverId，命中 /agent/commands；token 头存在。
        val req = transport.lastRequest.get()!!
        assertTrue(req.url.contains("/agent/commands"))
        assertTrue(req.url.contains("namespace=prod"))
        assertTrue(req.url.contains("serverId=lobby-1"))
        assertEquals("tk", req.headers[BeaconApiClient.HEADER_TOKEN])
    }

    @Test
    fun `fetchPendingCommand 204 无待办返回 null`() {
        val transport = ScriptTransport().apply { commandsStatus = 204 }
        val client = BeaconApiClient(transport, CmdCodec(), settings())
        assertNull(client.fetchPendingCommand(identity()), "204 应返回 null（无待办命令）")
    }

    @Test
    fun `fetchPendingCommand 非 200 204 返回 null`() {
        val transport = ScriptTransport().apply { commandsStatus = 404 }
        val client = BeaconApiClient(transport, CmdCodec(), settings())
        assertNull(client.fetchPendingCommand(identity()), "404 等应返回 null（best-effort 放弃本轮）")
    }

    @Test
    fun `uploadIngest 报文为 commandId 与 files 数组`() {
        val transport = ScriptTransport().apply { ingestStatus = 200 }
        val codec = CmdCodec()
        val client = BeaconApiClient(transport, codec, settings())

        val ok = client.uploadIngest(
            commandId = 42L,
            files = listOf(
                IngestFile("config.yml", "k: v"),
                IngestFile("lang/zh.yml", "hi: 你好"),
            ),
        )
        assertTrue(ok, "200 应返回 true")
        // 命中 ingest 端点。
        assertTrue(transport.lastRequest.get()!!.url.contains("/agent/files/ingest"))

        @Suppress("UNCHECKED_CAST")
        val body = codec.lastEncoded.get() as Map<String, Any?>
        assertEquals(42L, body["commandId"])
        @Suppress("UNCHECKED_CAST")
        val files = body["files"] as List<Map<String, Any?>>
        assertEquals(2, files.size)
        assertEquals("config.yml", files[0]["path"])
        assertEquals("k: v", files[0]["content"])
        assertEquals("lang/zh.yml", files[1]["path"])
        assertEquals("hi: 你好", files[1]["content"])
        // 报文键集合精确（与控制面 Go 侧契约对齐）。
        assertEquals(setOf("commandId", "files"), body.keys)
    }

    @Test
    fun `uploadIngest 非 200 返回 false`() {
        val transport = ScriptTransport().apply { ingestStatus = 409 }
        val client = BeaconApiClient(transport, CmdCodec(), settings())
        val ok = client.uploadIngest(7L, listOf(IngestFile("a.yml", "x")))
        assertFalse(ok, "非 200（命令态不符 / 校验拒）应返回 false")
    }

    // ---- 两段式 scan/submit 契约（FR-58，见 ADR-0037） ----

    @Test
    fun `fetchPendingCommand 解析 scan mode`() {
        val transport = ScriptTransport().apply {
            commandsStatus = 200
            commandsBody = BODY_SCAN_CMD
        }
        val client = BeaconApiClient(transport, CmdCodec(), settings())
        val cmd = client.fetchPendingCommand(identity())
        assertEquals(43L, cmd?.id)
        assertEquals("scan", cmd?.payload?.mode)
        assertTrue(cmd?.payload?.selectedPaths?.isEmpty() == true, "scan 无选定集")
    }

    @Test
    fun `fetchPendingCommand 解析 submit mode 与 selectedPaths`() {
        val transport = ScriptTransport().apply {
            commandsStatus = 200
            commandsBody = BODY_SUBMIT_CMD
        }
        val client = BeaconApiClient(transport, CmdCodec(), settings())
        val cmd = client.fetchPendingCommand(identity())
        assertEquals(44L, cmd?.id)
        assertEquals("submit", cmd?.payload?.mode)
        assertEquals(listOf("config.yml", "lang/zh.yml"), cmd?.payload?.selectedPaths)
    }

    @Test
    fun `fetchPendingCommand 旧命令无 mode 字段时为空 mode`() {
        val transport = ScriptTransport().apply {
            commandsStatus = 200
            commandsBody = BODY_INGEST_CMD // 无 mode / selectedPaths
        }
        val client = BeaconApiClient(transport, CmdCodec(), settings())
        val cmd = client.fetchPendingCommand(identity())
        assertEquals("", cmd?.payload?.mode, "旧命令缺 mode → 空串（向后兼容旧整树行为）")
        assertTrue(cmd?.payload?.selectedPaths?.isEmpty() == true)
    }

    @Test
    fun `uploadScan 报文为 commandId 与 files 元信息数组无内容`() {
        val transport = ScriptTransport().apply { scanStatus = 200 }
        val codec = CmdCodec()
        val client = BeaconApiClient(transport, codec, settings())

        val ok = client.uploadScan(
            commandId = 43L,
            files = listOf(
                ScanFile("config.yml", 100L, isText = true, overThreshold = false),
                ScanFile("metrics.jsonl", 2_000_000L, isText = true, overThreshold = true),
            ),
        )
        assertTrue(ok, "200 应返回 true")
        // 命中 scan 端点（POST /agent/files/scan）。
        assertTrue(transport.lastRequest.get()!!.url.contains("/agent/files/scan"))

        @Suppress("UNCHECKED_CAST")
        val body = codec.lastEncoded.get() as Map<String, Any?>
        assertEquals(43L, body["commandId"])
        @Suppress("UNCHECKED_CAST")
        val files = body["files"] as List<Map<String, Any?>>
        assertEquals(2, files.size)
        assertEquals("config.yml", files[0]["path"])
        assertEquals(100L, files[0]["size"])
        assertEquals(true, files[0]["isText"])
        assertEquals(false, files[0]["overThreshold"])
        assertEquals(true, files[1]["overThreshold"], "超阈值文件 overThreshold=true")
        // 报文键集合精确：每个文件项仅 path/size/isText/overThreshold，**无 content**（与控制面 /scan 契约对齐）。
        assertEquals(setOf("path", "size", "isText", "overThreshold"), files[0].keys)
        assertEquals(setOf("commandId", "files"), body.keys)
    }

    @Test
    fun `uploadScan 非 200 返回 false`() {
        val transport = ScriptTransport().apply { scanStatus = 409 }
        val client = BeaconApiClient(transport, CmdCodec(), settings())
        val ok = client.uploadScan(7L, listOf(ScanFile("a.yml", 1L, isText = true, overThreshold = false)))
        assertFalse(ok, "非 200 应返回 false")
    }

    companion object {
        /** decode 路由用的命令 body 标记。 */
        private const val BODY_INGEST_CMD = "ingest-cmd"
        private const val BODY_SCAN_CMD = "scan-cmd"
        private const val BODY_SUBMIT_CMD = "submit-cmd"
    }
}
