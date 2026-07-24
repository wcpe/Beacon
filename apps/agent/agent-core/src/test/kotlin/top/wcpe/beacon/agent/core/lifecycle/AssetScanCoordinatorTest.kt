package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.command.AssetEntry
import top.wcpe.beacon.agent.core.command.AssetManifestDigest
import top.wcpe.beacon.agent.core.filetree.AssetManifestStore
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.BackoffSettings
import top.wcpe.beacon.agent.core.settings.FileTreeSettings
import top.wcpe.beacon.agent.core.settings.OverrideSettings
import top.wcpe.beacon.agent.core.testsupport.ManualAsyncAdapter
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.io.File
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicInteger
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotNull
import kotlin.test.assertTrue

/**
 * 文件资产扫描协调器 [AssetScanCoordinator] 确定性单测（FR-163，见 ADR asset-manifest-sync-protocol）。
 *
 * 用手动调度适配器（延迟任务入队、测试 drainOne 推进）+ 真 [BeaconApiClient] 经忠实内存控制面桩驱动周期：
 * - 首次扫描发全量并落库；
 * - 无变更周期不发请求、每 6 个周期发一次空 delta 保活；
 * - 文件变更发 delta（正确 upserts / deleted）并收敛；
 * - delta 基线失配 409 → 本周期改发全量收敛；
 * - 控制面不可达 → fail-static（不落新库、不崩），恢复后收敛。
 *
 * [RegistryCodec] 让请求 / 响应对象在桩与 client 间无损往返，使内存控制面能真算摘要、真跑增量收敛。
 */
class AssetScanCoordinatorTest {
    private val serverRoot: File = Files.createTempDirectory("beacon-asset-coord").toFile()
    private val storeDir: File = Files.createTempDirectory("beacon-asset-store").toFile()
    private val storeFile: File = File(storeDir, "asset-manifest.json")
    private val adapter = ManualAsyncAdapter(serverRoot)
    private val codec = RegistryCodec()
    private val cp = FakeAssetControlPlane(codec)

    @AfterTest
    fun cleanup() {
        serverRoot.deleteRecursively()
        storeDir.deleteRecursively()
    }

    private fun write(
        rel: String,
        content: String,
    ) {
        val f = File(serverRoot, rel)
        f.parentFile.mkdirs()
        f.writeText(content, StandardCharsets.UTF_8)
    }

    private fun readStore() = AssetManifestStore(storeFile, codec).read()

    private fun coordinator(): AssetScanCoordinator =
        AssetScanCoordinator(
            adapter = adapter,
            apiClient = BeaconApiClient(cp, codec, settings()),
            identity = identity(),
            store = AssetManifestStore(storeFile, codec),
            // 本地缓存落 storeDir（在 serverRoot 之外），故 selfDataDir 传 storeDir 不影响 serverRoot 下候选。
            scope = AssetScanScope(serverRoot = { serverRoot }, selfDataDir = storeDir),
            intervalMs = 60_000L,
        )

    @Test
    fun `首次扫描发全量并落库`() {
        write("plugins/a.yml", "k: v")
        coordinator().start()
        adapter.drainOne() // 周期 1

        assertEquals(1, cp.fullCalls.get(), "首次应全量上报")
        assertEquals("full", cp.lastMode)
        assertTrue(cp.lastEof, "单分片全量末批 eof=true")
        assertEquals(0, cp.deltaCalls.get())
        val stored = readStore()
        assertNotNull(stored, "全量受理后应落本地缓存")
        assertEquals(cp.currentDigest, stored.confirmedDigest, "落库摘要应与控制面确认摘要一致")
        assertTrue(stored.entries.any { it.path == "plugins/a.yml" })
    }

    @Test
    fun `无变更周期不发请求 第6周期发空delta保活`() {
        write("plugins/a.yml", "k: v")
        coordinator().start()
        adapter.drainOne() // 周期 1：全量 + 落库
        assertEquals(1, cp.fullCalls.get())

        repeat(5) { adapter.drainOne() } // 周期 2..6：5 个无变更周期
        assertEquals(0, cp.deltaCalls.get(), "前 5 个无变更周期不发任何请求")
        assertEquals(1, cp.manifestCalls.get(), "累计仅周期 1 的一次全量请求")

        adapter.drainOne() // 周期 7：第 6 个无变更周期 → 空 delta 保活
        assertEquals(1, cp.deltaCalls.get(), "第 6 个无变更周期发一次空 delta 保活")
        assertTrue(cp.lastUpsertPaths.isEmpty() && cp.lastDeletedPaths.isEmpty(), "保活 delta 应空 upserts / deleted")
    }

    @Test
    fun `文件变更发delta含正确upserts与deleted并收敛`() {
        write("plugins/a.yml", "k: v")
        write("plugins/b.yml", "x: y")
        coordinator().start()
        adapter.drainOne() // 周期 1：全量 {a,b}

        write("plugins/a.yml", "k: v-CHANGED") // 改
        write("plugins/c.yml", "new: 1") // 增
        File(serverRoot, "plugins/b.yml").delete() // 删
        adapter.drainOne() // 周期 2：delta

        assertEquals(1, cp.deltaCalls.get())
        assertEquals("delta", cp.lastMode)
        assertTrue(cp.lastUpsertPaths.containsAll(setOf("plugins/a.yml", "plugins/c.yml")), "改 / 增文件应在 upserts")
        assertFalse(cp.lastUpsertPaths.contains("plugins/b.yml"), "已删文件不应在 upserts")
        assertEquals(setOf("plugins/b.yml"), cp.lastDeletedPaths.toSet(), "已删文件应在 deleted")
        val stored = readStore()!!
        assertEquals(cp.currentDigest, stored.confirmedDigest, "delta 后应收敛落库")
        assertTrue(stored.entries.any { it.path == "plugins/c.yml" })
        assertFalse(stored.entries.any { it.path == "plugins/b.yml" })
    }

    @Test
    fun `delta基线失配409本周期改发全量收敛`() {
        write("plugins/a.yml", "k: v")
        coordinator().start()
        adapter.drainOne() // 周期 1：全量 + 落库

        write("plugins/a.yml", "k: v2")
        cp.forceNextDeltaConflict = true // 令下一次 delta 返回 409
        adapter.drainOne() // 周期 2：delta → 409 → 改发全量

        assertEquals(1, cp.deltaCalls.get(), "先发一次 delta")
        assertEquals(2, cp.fullCalls.get(), "delta 409 后本周期改发全量（初始 1 + 重试 1）")
        assertEquals(cp.currentDigest, readStore()!!.confirmedDigest, "全量重试后应收敛落库")
    }

    @Test
    fun `控制面不可达fail-static不落新库不崩恢复后收敛`() {
        write("plugins/a.yml", "k: v")
        coordinator().start()
        adapter.drainOne() // 周期 1：全量 + 落库
        val confirmedBefore = readStore()!!.confirmedDigest

        write("plugins/a.yml", "k: v2")
        cp.failConnection = true
        adapter.drainOne() // 周期 2：delta 尝试 → 连接失败 → fail-static（不落新库、不抛）
        assertEquals(confirmedBefore, readStore()!!.confirmedDigest, "不可达时保留旧库、不落新库")

        cp.failConnection = false
        adapter.drainOne() // 周期 3：恢复 → delta 收敛
        val stored = readStore()!!
        assertTrue(stored.confirmedDigest != confirmedBefore, "恢复后应上报新变更并落新库")
        assertEquals(cp.currentDigest, stored.confirmedDigest, "恢复后应与控制面收敛")
    }

    @Test
    fun `forceScanNow立即全量上报并落库`() {
        write("plugins/a.yml", "k: v")
        val dispatched = coordinator().forceScanNow(force = true)

        assertTrue(dispatched, "手动重扫应返回已派发")
        assertEquals(1, cp.fullCalls.get(), "手动重扫应全量上报")
        assertEquals("full", cp.lastMode)
        assertNotNull(readStore(), "手动重扫受理后应落本地缓存")
    }

    private fun settings() =
        AgentSettings(
            endpoints = listOf("http://localhost:8848"),
            bootstrapToken = "tk",
            pollTimeoutMs = 50,
            requestTimeoutMs = 200,
            heartbeatFallbackMs = 100_000,
            backoff = BackoffSettings(initialMs = 60_000, maxMs = 60_000, multiplier = 1.0, jitterRatio = 0.0),
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
            identityId = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
            bootId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
        )

    /**
     * 双向对象注册 codec：encode 把对象登记返 id、decode 按 id 取回——使请求 / 响应对象在桩与 client 间无损往返，
     * 本地清单缓存文件也经此存取（内容即 id）。供内存控制面读取真实上报体、算真实摘要。
     */
    private class RegistryCodec : JsonCodec {
        private val store = ConcurrentHashMap<String, Any?>()
        private val counter = AtomicInteger(0)

        override fun encode(value: Any?): String {
            val id = "obj-${counter.incrementAndGet()}"
            if (value != null) store[id] = value
            return id
        }

        override fun decode(json: String): Any? = store[json]

        fun objectOf(id: String): Any? = store[id]
    }

    /**
     * 忠实内存控制面桩：按 §4.3 协议维护每服清单 + 摘要，真算增量收敛。
     * full eof 整体替换后重算摘要；delta 校验 baseDigest（失配返 409）后应用 upserts / deleted 再重算。
     */
    private class FakeAssetControlPlane(private val codec: RegistryCodec) : HttpTransport {
        val manifestCalls = AtomicInteger(0)
        val fullCalls = AtomicInteger(0)
        val deltaCalls = AtomicInteger(0)

        @Volatile
        var lastMode: String = ""

        @Volatile
        var lastEof: Boolean = false
        var lastUpsertPaths: List<String> = emptyList()
        var lastDeletedPaths: List<String> = emptyList()

        /** 令下一次 delta 返回 409（模拟基线漂移）。 */
        @Volatile
        var forceNextDeltaConflict: Boolean = false

        /** 令 /assets/manifest 请求抛连接异常（模拟控制面不可达）。 */
        @Volatile
        var failConnection: Boolean = false

        private val manifest = LinkedHashMap<String, AssetEntry>()
        private val shardBuffers = HashMap<String, LinkedHashMap<String, AssetEntry>>()

        @Volatile
        var currentDigest: String = ""

        override fun execute(request: HttpRequest): HttpResponse {
            if (failConnection) throw java.io.IOException("控制面不可达（测试模拟）")
            if (!request.url.contains("/assets/manifest")) return HttpResponse(404, codec.encode(emptyMap<String, Any?>()))
            manifestCalls.incrementAndGet()
            val body = asMap(codec.objectOf(request.body ?: ""))
            lastMode = body["mode"] as? String ?: ""
            val upserts = upsertsOf(body)
            return if (lastMode == "full") handleFull(body, upserts) else handleDelta(body, upserts)
        }

        private fun handleFull(
            body: Map<String, Any?>,
            upserts: List<AssetEntry>,
        ): HttpResponse {
            fullCalls.incrementAndGet()
            lastUpsertPaths = upserts.map { it.path }
            lastDeletedPaths = emptyList()
            val uploadId = body["uploadId"] as? String ?: ""
            val eof = body["eof"] as? Boolean ?: false
            lastEof = eof
            val buffer = shardBuffers.getOrPut(uploadId) { LinkedHashMap() }
            upserts.forEach { buffer[it.path] = it }
            if (!eof) return accepted(digest = "", fileCount = buffer.size)
            manifest.clear()
            manifest.putAll(buffer)
            shardBuffers.remove(uploadId)
            currentDigest = AssetManifestDigest.computeManifestDigest(manifest.values.toList())
            return accepted(currentDigest, manifest.size)
        }

        private fun handleDelta(
            body: Map<String, Any?>,
            upserts: List<AssetEntry>,
        ): HttpResponse {
            deltaCalls.incrementAndGet()
            lastUpsertPaths = upserts.map { it.path }
            @Suppress("UNCHECKED_CAST")
            lastDeletedPaths = (body["deleted"] as? List<String>) ?: emptyList()
            // 基线失配（强制冲突 / baseDigest 不匹配）→ 409。
            val conflict = forceNextDeltaConflict || (body["baseDigest"] as? String ?: "") != currentDigest
            forceNextDeltaConflict = false
            if (conflict) return HttpResponse(409, codec.encode(mapOf("code" to "asset_manifest_out_of_sync")))
            upserts.forEach { manifest[it.path] = it }
            lastDeletedPaths.forEach { manifest.remove(it) }
            currentDigest = AssetManifestDigest.computeManifestDigest(manifest.values.toList())
            return accepted(currentDigest, manifest.size)
        }

        private fun accepted(
            digest: String,
            fileCount: Int,
        ): HttpResponse = HttpResponse(200, codec.encode(mapOf("digest" to digest, "fileCount" to fileCount)))

        @Suppress("UNCHECKED_CAST")
        private fun asMap(value: Any?): Map<String, Any?> = (value as? Map<String, Any?>) ?: emptyMap()

        @Suppress("UNCHECKED_CAST")
        private fun upsertsOf(body: Map<String, Any?>): List<AssetEntry> =
            (body["upserts"] as? List<Map<String, Any?>> ?: emptyList()).map { m ->
                AssetEntry(
                    path = m["path"] as String,
                    sha256 = m["sha256"] as String,
                    size = (m["size"] as Number).toLong(),
                    mtimeMs = (m["mtimeMs"] as Number).toLong(),
                    isText = m["isText"] as Boolean,
                )
            }
    }
}
