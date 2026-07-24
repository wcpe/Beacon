package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.command.AssetEntry
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
import kotlin.test.assertIs
import kotlin.test.assertTrue

/**
 * BeaconApiClient.reportAssetManifest 契约单测（FR-163 §5.1，见 ADR asset-manifest-sync-protocol）。
 *
 * 锁定：v2 端点路径、鉴权头（X-Beacon-Token + X-Beacon-Identity）、delta / full 报文信封键集与 upserts 元素键集
 * （全 camelCase，与控制面接收结构体对齐）、状态码 → outcome 映射（200/409/400/连接失败）。
 */
class BeaconApiClientAssetManifestTest {
    private class CapturingCodec(private val decodeBody: (String) -> Any?) : JsonCodec {
        val lastEncoded = AtomicReference<Any?>(null)

        override fun encode(value: Any?): String {
            lastEncoded.set(value)
            return "encoded"
        }

        override fun decode(json: String): Any? = decodeBody(json)
    }

    private class StatusTransport(private val status: Int, private val body: String = "") : HttpTransport {
        val lastRequest = AtomicReference<HttpRequest>()

        override fun execute(request: HttpRequest): HttpResponse {
            lastRequest.set(request)
            return HttpResponse(status, body)
        }
    }

    private class ThrowingTransport : HttpTransport {
        override fun execute(request: HttpRequest): HttpResponse = throw java.io.IOException("connection refused")
    }

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
            identityId = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
            bootId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
        )

    private fun entry() =
        AssetEntry(
            path = "plugins/Foo/config.yml",
            sha256 = "a".repeat(64),
            size = 12,
            mtimeMs = 1700000000000L,
            isText = true,
        )

    @Suppress("UNCHECKED_CAST")
    private fun envelope(codec: CapturingCodec): Map<String, Any?> = codec.lastEncoded.get() as Map<String, Any?>

    @Suppress("UNCHECKED_CAST")
    private fun firstUpsert(codec: CapturingCodec): Map<String, Any?> = (envelope(codec)["upserts"] as List<Map<String, Any?>>).first()

    @Test
    fun `delta 报文信封与鉴权头符合契约`() {
        val codec = CapturingCodec { mapOf("digest" to "d1", "fileCount" to 3) }
        val transport = StatusTransport(200)
        val outcome =
            BeaconApiClient(transport, codec, settings()).reportAssetManifest(
                identity = identity(),
                meta =
                    AssetManifestMeta(
                        mode = "delta",
                        scannedAtMs = 1700000000000L,
                        scanDurationMs = 42L,
                        truncated = false,
                        upserts = listOf(entry()),
                    ),
                baseDigest = "base-d",
                deleted = listOf("plugins/old.yml"),
            )
        val acc = assertIs<AssetManifestOutcome.Accepted>(outcome)
        assertEquals("d1", acc.digest)
        assertEquals(3, acc.fileCount)

        val req = transport.lastRequest.get()
        assertTrue(req.url.endsWith("/beacon/v2/agent/assets/manifest"), "应打 v2 资产清单端点")
        assertEquals("tk", req.headers[BeaconApiClient.HEADER_TOKEN])
        assertEquals("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", req.headers[BeaconApiClient.HEADER_IDENTITY])
        val body = envelope(codec)
        assertEquals(
            setOf("mode", "scannedAt", "scanDurationMs", "truncated", "upserts", "baseDigest", "deleted"),
            body.keys,
            "delta 信封键集须与契约一致（无 uploadId / seq / eof）",
        )
        assertEquals("delta", body["mode"])
        assertEquals("base-d", body["baseDigest"])
        assertEquals(listOf("plugins/old.yml"), body["deleted"])
        assertEquals(java.time.Instant.ofEpochMilli(1700000000000L).toString(), body["scannedAt"])
        assertEquals(
            setOf("path", "sha256", "size", "mtimeMs", "isText"),
            firstUpsert(codec).keys,
            "upserts 元素键集须为 camelCase",
        )
    }

    @Test
    fun `full 报文含 uploadId seq eof 无 baseDigest deleted`() {
        val codec = CapturingCodec { mapOf("digest" to "final", "fileCount" to 1) }
        BeaconApiClient(StatusTransport(200), codec, settings()).reportAssetManifest(
            identity = identity(),
            meta = AssetManifestMeta(mode = "full", scannedAtMs = 1L, scanDurationMs = 10L, truncated = true, upserts = listOf(entry())),
            uploadId = "u-1",
            seq = 2,
            eof = true,
        )
        val body = envelope(codec)
        assertEquals(
            setOf("mode", "scannedAt", "scanDurationMs", "truncated", "upserts", "uploadId", "seq", "eof"),
            body.keys,
            "full 信封键集须与契约一致（无 baseDigest / deleted）",
        )
        assertEquals("full", body["mode"])
        assertEquals("u-1", body["uploadId"])
        assertEquals(2, body["seq"])
        assertEquals(true, body["eof"])
        assertEquals(true, body["truncated"])
        assertFalse(body.containsKey("baseDigest"))
        assertFalse(body.containsKey("deleted"))
    }

    @Test
    fun `409 为基线失配 OutOfSync`() {
        val codec = CapturingCodec { emptyMap<String, Any?>() }
        val outcome =
            BeaconApiClient(StatusTransport(409, "err"), codec, settings())
                .reportAssetManifest(identity(), AssetManifestMeta("delta", 1L, 1L, false, listOf(entry())), baseDigest = "x")
        assertIs<AssetManifestOutcome.OutOfSync>(outcome)
    }

    @Test
    fun `400 携脱敏原因码 Rejected`() {
        val codec = CapturingCodec { mapOf("code" to "invalid_param", "message" to "参数非法") }
        val outcome =
            BeaconApiClient(StatusTransport(400, "err"), codec, settings())
                .reportAssetManifest(
                    identity(),
                    AssetManifestMeta("full", 1L, 1L, false, listOf(entry())),
                    uploadId = "u",
                    seq = 0,
                    eof = true,
                )
        val rejected = assertIs<AssetManifestOutcome.Rejected>(outcome)
        assertEquals("invalid_param", rejected.code)
    }

    @Test
    fun `连接失败为 Failed`() {
        val codec = CapturingCodec { emptyMap<String, Any?>() }
        val outcome =
            BeaconApiClient(ThrowingTransport(), codec, settings())
                .reportAssetManifest(
                    identity(),
                    AssetManifestMeta("full", 1L, 1L, false, listOf(entry())),
                    uploadId = "u",
                    seq = 0,
                    eof = true,
                )
        assertIs<AssetManifestOutcome.Failed>(outcome)
    }
}
