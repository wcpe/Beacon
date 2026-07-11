package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.sampling.BackendBatch
import top.wcpe.beacon.agent.core.sampling.LoadAgg
import top.wcpe.beacon.agent.core.sampling.MetricBatch
import top.wcpe.beacon.agent.core.sampling.MetricKind
import top.wcpe.beacon.agent.core.sampling.ProxyBatch
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
import kotlin.test.assertIs
import kotlin.test.assertTrue

/**
 * BeaconApiClient.reportMetricsBatch 契约单测（FR-144 §5.1）。
 *
 * 锁定：v2 端点路径、鉴权头（X-Beacon-Token + X-Beacon-Identity）、报文信封与 samples 元素键集
 * （snake_case §3.1）、不适用维度缺省值、状态码 → outcome 映射（202/429/403/400）。
 */
class BeaconApiClientMetricsBatchTest {
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

    private fun backendBatch() =
        MetricBatch(
            bucketStartMs = 5000L,
            sampleCount = 5,
            load = LoadAgg(cpuPctAvg = 40.0, cpuPctMax = 55.0, memUsedMbAvg = 512.0, memMaxMb = 2048),
            reportRttMs = 12,
            payload = BackendBatch(tpsAvg = 19.5, tpsMin = 18.0, onlineAvg = 30, onlineMax = 40, maxOnline = 100),
        )

    private fun proxyBatch() =
        MetricBatch(
            bucketStartMs = 5000L,
            sampleCount = 5,
            load = LoadAgg(cpuPctAvg = 20.0, cpuPctMax = 25.0, memUsedMbAvg = 256.0, memMaxMb = 1024),
            reportRttMs = 7,
            payload = ProxyBatch(connAvg = 120, connMax = 150, backendUp = 3, backendTotal = 4, backendRttMsAvg = 9.0),
        )

    @Suppress("UNCHECKED_CAST")
    private fun envelope(codec: CapturingCodec): Map<String, Any?> = codec.lastEncoded.get() as Map<String, Any?>

    @Suppress("UNCHECKED_CAST")
    private fun firstSample(codec: CapturingCodec): Map<String, Any?> = (envelope(codec)["samples"] as List<Map<String, Any?>>).first()

    @Test
    fun `信封键与鉴权头符合契约`() {
        val codec = CapturingCodec { mapOf("accepted" to 5, "deduplicated" to 0) }
        val transport = StatusTransport(202)
        val outcome =
            BeaconApiClient(transport, codec, settings())
                .reportMetricsBatch(
                    identity(),
                    MetricKind.BACKEND,
                    agentTimeMs = 123_456L,
                    droppedSinceLast = 2L,
                    batches = listOf(backendBatch()),
                )

        assertIs<MetricsReportOutcome.Accepted>(outcome)
        val req = transport.lastRequest.get()
        assertTrue(req.url.endsWith("/beacon/v2/agent/metrics/report"), "应打 v2 指标端点")
        assertEquals("tk", req.headers[BeaconApiClient.HEADER_TOKEN])
        assertEquals("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", req.headers[BeaconApiClient.HEADER_IDENTITY])
        val body = envelope(codec)
        assertEquals(
            setOf("namespace", "serverId", "kind", "agentTimeMs", "droppedSinceLast", "samples"),
            body.keys,
            "信封键集合须与契约一致",
        )
        assertEquals("backend", body["kind"])
        assertEquals(123_456L, body["agentTimeMs"])
        assertEquals(2L, body["droppedSinceLast"])
    }

    @Test
    fun `backend samples 元素键集与缺省值`() {
        val codec = CapturingCodec { mapOf("accepted" to 1) }
        BeaconApiClient(StatusTransport(202), codec, settings())
            .reportMetricsBatch(identity(), MetricKind.BACKEND, agentTimeMs = 1L, droppedSinceLast = 0L, batches = listOf(backendBatch()))

        val s = firstSample(codec)
        assertEquals(
            setOf(
                "bucket_start_ms",
                "sample_count",
                "cpu_pct_avg",
                "cpu_pct_max",
                "mem_used_mb_avg",
                "mem_max_mb",
                "tps_avg",
                "tps_min",
                "online_avg",
                "online_max",
                "max_online",
                "conn_avg",
                "conn_max",
                "backend_up",
                "backend_total",
                "backend_rtt_ms_avg",
                "report_rtt_ms",
            ),
            s.keys,
            "samples 元素键集合须与 §3.1 列一致",
        )
        assertEquals(5000L, s["bucket_start_ms"])
        assertEquals(5, s["sample_count"])
        assertEquals(19.5, s["tps_avg"])
        assertEquals(40, s["online_max"])
        assertEquals(12, s["report_rtt_ms"])
        // backend 行 proxy 维度写缺省：连接 / 后端 0，后端 RTT -1。
        assertEquals(0, s["conn_avg"])
        assertEquals(0, s["backend_up"])
        assertEquals(-1.0, s["backend_rtt_ms_avg"])
    }

    @Test
    fun `proxy samples 元素 backend 维度写缺省`() {
        val codec = CapturingCodec { mapOf("accepted" to 1) }
        BeaconApiClient(StatusTransport(202), codec, settings())
            .reportMetricsBatch(identity(), MetricKind.PROXY, agentTimeMs = 1L, droppedSinceLast = 0L, batches = listOf(proxyBatch()))

        val s = firstSample(codec)
        assertEquals(120, s["conn_avg"])
        assertEquals(3, s["backend_up"])
        assertEquals(9.0, s["backend_rtt_ms_avg"])
        // proxy 行 backend 维度写缺省 0。
        assertEquals(0.0, s["tps_avg"])
        assertEquals(0, s["online_max"])
        assertEquals(0, s["max_online"])
    }

    @Test
    fun `202 回报受理与去重计数`() {
        val codec = CapturingCodec { mapOf("accepted" to 4, "deduplicated" to 1) }
        val outcome =
            BeaconApiClient(StatusTransport(202), codec, settings())
                .reportMetricsBatch(identity(), MetricKind.BACKEND, 1L, 0L, listOf(backendBatch()))
        val acc = assertIs<MetricsReportOutcome.Accepted>(outcome)
        assertEquals(4, acc.accepted)
        assertEquals(1, acc.deduplicated)
        assertTrue(acc.rttMs >= 0, "应带非负上报 RTT")
    }

    @Test
    fun `429 为忙 403 为未确认`() {
        val codec = CapturingCodec { emptyMap<String, Any?>() }
        val busy =
            BeaconApiClient(StatusTransport(429), codec, settings())
                .reportMetricsBatch(identity(), MetricKind.BACKEND, 1L, 0L, listOf(backendBatch()))
        assertIs<MetricsReportOutcome.Busy>(busy)
        val forbidden =
            BeaconApiClient(StatusTransport(403), codec, settings())
                .reportMetricsBatch(identity(), MetricKind.BACKEND, 1L, 0L, listOf(backendBatch()))
        assertIs<MetricsReportOutcome.Forbidden>(forbidden)
    }

    @Test
    fun `400 携带脱敏原因码`() {
        val codec = CapturingCodec { mapOf("code" to "clock_skew_too_large", "message" to "时钟偏移过大") }
        val outcome =
            BeaconApiClient(StatusTransport(400, "err"), codec, settings())
                .reportMetricsBatch(identity(), MetricKind.BACKEND, 1L, 0L, listOf(backendBatch()))
        val rejected = assertIs<MetricsReportOutcome.Rejected>(outcome)
        assertEquals("clock_skew_too_large", rejected.reason)
    }
}
