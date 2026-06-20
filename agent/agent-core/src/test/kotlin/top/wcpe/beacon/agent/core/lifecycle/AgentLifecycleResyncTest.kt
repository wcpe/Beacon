package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.config.ConfigApplier
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.filetree.AppliedFileManifestStore
import top.wcpe.beacon.agent.core.filetree.FileMirrorWriter
import top.wcpe.beacon.agent.core.filetree.FileTreeApplier
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.BackoffSettings
import top.wcpe.beacon.agent.core.settings.FileTreeSettings
import top.wcpe.beacon.agent.core.settings.OverrideSettings
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import top.wcpe.beacon.agent.core.testutil.ThreadPoolPlatformAdapter
import java.io.File
import java.nio.file.Files
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * AgentLifecycle.forceSyncFileTreeNow（resync 真接通，FR-17）单测：
 * - 文件树子系统启用时：返回 true，旁路长轮询以空 fileTreeMd5 强制拉一次清单并由 applier 幂等落盘；
 * - 文件树子系统未启用（fileTreeApplier 为 null）时：返回 false，且不触发任何清单拉取。
 */
class AgentLifecycleResyncTest {

    private val store = EffectiveConfigStore()

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
        backoff = BackoffSettings(initialMs = 60_000, maxMs = 60_000, multiplier = 1.0, jitterRatio = 0.0),
        snapshotEnabled = false,
        snapshotFileName = "snapshot.json",
        fileTree = FileTreeSettings(enabled = true, targetSubDir = "", appliedManifestFileName = "file-tree.applied.json"),
        override = OverrideSettings(commandWhitelist = emptySet(), backupDirName = "override-backup"),
    )

    @AfterTest
    fun tearDown() {
        adapter?.shutdown()
    }

    private var adapter: ThreadPoolPlatformAdapter? = null

    @Test
    fun `resync 启用时返回已触发 并以空 md5 强制拉清单落盘`() {
        val dataFolder = Files.createTempDirectory("beacon-resync-data").toFile()
        val mirrorRoot = Files.createTempDirectory("beacon-resync-mirror").toFile()
        val adapterLocal = ThreadPoolPlatformAdapter(folder = dataFolder)
        adapter = adapterLocal

        val backend = FileTreeBackend()
        val codec = FileTreeCodec()
        val apiClient = BeaconApiClient(backend, codec, settings())
        val fileTreeApplier = FileTreeApplier(
            mirrorWriter = FileMirrorWriter(mirrorRoot),
            appliedStore = AppliedFileManifestStore(File(dataFolder, "file-tree.applied.json"), codec),
            adapter = adapterLocal,
            fetchContent = { path -> apiClient.fetchFileContent(identity(), path) },
        )
        val applier = ConfigApplier(store, null, adapterLocal)
        val lifecycle = AgentLifecycle(
            identity(), settings(), adapterLocal, apiClient, store, applier, null,
            fileTreeApplier = fileTreeApplier,
        )
        // 先接入控制面置 running=true（resync 仅在运行期生效）；文件树长轮询循环此后续杯。
        lifecycle.bootstrapWithSnapshotThenConnect()
        waitUntil(2000) { lifecycle.currentState() == AgentState.RUNNING }

        // 文件树子系统已启用：resync 应返回 true（已触发）。
        val triggered = lifecycle.forceSyncFileTreeNow()
        assertTrue(triggered, "文件树子系统已启用，resync 应返回已触发")

        // 异步落盘应把清单内的文件镜像到目标根（证明 resync 真触发同步，而非占位文案）。
        val target = File(mirrorRoot, "demo.yml")
        waitUntil(2000) { target.exists() }
        assertTrue(target.exists(), "resync 应把清单文件镜像落盘")
        assertEquals("k: v\n", target.readText())
        // resync 旁路长轮询 304，以空 fileTreeMd5 强制拉清单。
        assertTrue(backend.emptyMd5ManifestCalls.get() >= 1, "resync 必须以空 md5 强制拉清单")
    }

    @Test
    fun `resync 未启用文件树时返回未触发 且不拉清单`() {
        val adapterLocal = ThreadPoolPlatformAdapter()
        adapter = adapterLocal

        val backend = FileTreeBackend()
        val codec = FileTreeCodec()
        val apiClient = BeaconApiClient(backend, codec, settings())
        val applier = ConfigApplier(store, null, adapterLocal)
        // fileTreeApplier 为 null：文件树子系统未启用。
        val lifecycle = AgentLifecycle(
            identity(), settings(), adapterLocal, apiClient, store, applier, null,
            fileTreeApplier = null,
        )

        val triggered = lifecycle.forceSyncFileTreeNow()
        assertFalse(triggered, "文件树子系统未启用，resync 应返回未触发")
        // 不应触发任何清单拉取。
        Thread.sleep(150)
        assertEquals(0, backend.manifestCalls.get(), "未启用时不得拉清单")
    }

    private fun waitUntil(timeoutMs: Long, cond: () -> Boolean) {
        val deadline = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(timeoutMs)
        while (System.nanoTime() < deadline) {
            if (cond()) return
            Thread.sleep(10)
        }
    }
}

/**
 * 测试用文件树后端：按 URL 路由 files/manifest（200 单文件）与 files/content（200 文本）。
 * 记录清单调用数与「空 md5」清单调用数，供断言 resync 旁路 304 行为。
 */
private class FileTreeBackend : HttpTransport {

    val manifestCalls = AtomicInteger(0)
    val emptyMd5ManifestCalls = AtomicInteger(0)

    override fun execute(request: HttpRequest): HttpResponse {
        val url = request.url
        return when {
            url.endsWith("/register") -> HttpResponse(200, BODY_REGISTER)
            url.contains("/heartbeat") -> HttpResponse(200, BODY_HEARTBEAT)
            url.contains("/config/effective") -> HttpResponse(304, "")
            url.contains("/files/manifest") -> {
                manifestCalls.incrementAndGet()
                // 空 md5（首拉 / resync 强制）→ 200 返回清单；非空 md5（长轮询续杯）→ 304，避免测试内忙转。
                if (isEmptyMd5(url)) {
                    emptyMd5ManifestCalls.incrementAndGet()
                    HttpResponse(200, BODY_MANIFEST)
                } else {
                    HttpResponse(304, "")
                }
            }

            url.contains("/files/content") -> HttpResponse(200, BODY_CONTENT)
            else -> HttpResponse(404, "")
        }
    }

    /** manifest URL 的 md5 查询为空串视为「强制重拉」。 */
    private fun isEmptyMd5(url: String): Boolean {
        val idx = url.indexOf("md5=")
        if (idx < 0) return true
        val rest = url.substring(idx + 4)
        val end = rest.indexOf('&').let { if (it < 0) rest.length else it }
        return rest.substring(0, end).isEmpty()
    }

    companion object {
        const val BODY_REGISTER = "register-body"
        const val BODY_HEARTBEAT = "heartbeat-body"
        const val BODY_MANIFEST = "manifest-body"
        const val BODY_CONTENT = "content-body"
    }
}

/** 极简 codec：encode 走 JSON 序列化兜底（清单落盘需可读回），decode 按 body key 返回预置树。 */
private class FileTreeCodec : JsonCodec {

    override fun encode(value: Any?): String = renderJson(value)

    override fun decode(json: String): Any? = when (json) {
        FileTreeBackend.BODY_REGISTER -> mapOf(
            "instanceKey" to "prod/lobby-1",
            "resolvedGroup" to "area1",
            "resolvedZone" to "zoneA",
            "heartbeatIntervalSec" to 10,
            "ttlSec" to 30,
            "assigned" to true,
        )

        FileTreeBackend.BODY_HEARTBEAT -> mapOf("ttlSec" to 30, "configDirty" to false)

        FileTreeBackend.BODY_MANIFEST -> mapOf(
            "namespace" to "prod",
            "serverId" to "lobby-1",
            "group" to "area1",
            "zone" to "zoneA",
            "fileTreeMd5" to "ft-v1",
            "files" to listOf(mapOf("path" to "demo.yml", "md5" to "f1")),
        )

        FileTreeBackend.BODY_CONTENT -> mapOf(
            "path" to "demo.yml",
            "md5" to "f1",
            "content" to "k: v\n",
        )

        else -> JsonTreeReader.parse(json)
    }
}

/** 极简 JSON 序列化（仅覆盖测试中已落盘清单的 map/list/标量结构），供清单写回再读回。 */
private fun renderJson(value: Any?): String = when (value) {
    null -> "null"
    is String -> "\"" + value.replace("\\", "\\\\").replace("\"", "\\\"").replace("\n", "\\n") + "\""
    is Number, is Boolean -> value.toString()
    is Map<*, *> -> value.entries.joinToString(",", "{", "}") { (k, v) -> "\"$k\":" + renderJson(v) }
    is List<*> -> value.joinToString(",", "[", "]") { renderJson(it) }
    else -> "\"$value\""
}

/** 极简 JSON 解析（仅覆盖本测试写回的清单结构），把已落盘清单读回成 map。 */
private object JsonTreeReader {

    fun parse(json: String): Any? = Parser(json).parseValue()

    private class Parser(private val s: String) {
        private var i = 0

        fun parseValue(): Any? {
            skipWs()
            return when (s[i]) {
                '{' -> parseObject()
                '[' -> parseArray()
                '"' -> parseString()
                't' -> { i += 4; true }
                'f' -> { i += 5; false }
                'n' -> { i += 4; null }
                else -> parseNumber()
            }
        }

        private fun parseObject(): Map<String, Any?> {
            val map = LinkedHashMap<String, Any?>()
            i++ // {
            skipWs()
            if (s[i] == '}') { i++; return map }
            while (true) {
                skipWs()
                val key = parseString()
                skipWs()
                i++ // :
                val value = parseValue()
                map[key] = value
                skipWs()
                if (s[i] == ',') { i++; continue }
                i++ // }
                break
            }
            return map
        }

        private fun parseArray(): List<Any?> {
            val list = ArrayList<Any?>()
            i++ // [
            skipWs()
            if (s[i] == ']') { i++; return list }
            while (true) {
                list.add(parseValue())
                skipWs()
                if (s[i] == ',') { i++; continue }
                i++ // ]
                break
            }
            return list
        }

        private fun parseString(): String {
            i++ // 开引号
            val sb = StringBuilder()
            while (s[i] != '"') {
                if (s[i] == '\\') {
                    i++
                    when (s[i]) {
                        'n' -> sb.append('\n')
                        '"' -> sb.append('"')
                        '\\' -> sb.append('\\')
                        else -> sb.append(s[i])
                    }
                } else {
                    sb.append(s[i])
                }
                i++
            }
            i++ // 闭引号
            return sb.toString()
        }

        private fun parseNumber(): Any {
            val start = i
            while (i < s.length && (s[i].isDigit() || s[i] == '-' || s[i] == '.')) i++
            val text = s.substring(start, i)
            return if (text.contains('.')) text.toDouble() else text.toLong()
        }

        private fun skipWs() {
            while (i < s.length && s[i].isWhitespace()) i++
        }
    }
}
