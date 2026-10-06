package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.sampling.BackendBatch
import top.wcpe.beacon.agent.core.sampling.LoadAgg
import top.wcpe.beacon.agent.core.sampling.MetricBatch
import top.wcpe.beacon.agent.core.sampling.MetricKind
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
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * BeaconApiClient 调度端点契约单测（FR-148 §5.1）。
 *
 * 锁定：candidates / decide / report-local 三端点路径、鉴权头（X-Beacon-Token + X-Beacon-Identity）、
 * 请求体 camelCase 键、JsonTree 解析（含 chosen=null / failReason）、decide 800ms 读超时、状态码 → outcome 映射；
 * 以及指标上报 202 响应内 self 健康段的解析（selfHealth 数据源）。
 */
class BeaconApiClientSchedulingTest {
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

    @Suppress("UNCHECKED_CAST")
    private fun encoded(codec: CapturingCodec): Map<String, Any?> = codec.lastEncoded.get() as Map<String, Any?>

    @Suppress("UNCHECKED_CAST")
    private fun firstDecision(body: Map<String, Any?>): Map<String, Any?> = (body["decisions"] as List<Map<String, Any?>>).first()

    @Suppress("UNCHECKED_CAST")
    private fun firstExcluded(decision: Map<String, Any?>): Map<String, Any?> = (decision["excluded"] as List<Map<String, Any?>>).first()

    // ---- candidates ----

    @Test
    fun `candidates 200 解析候选快照并打 v2 端点鉴权头`() {
        val codec =
            CapturingCodec {
                mapOf(
                    "generatedAtMs" to 1_700_000_000_000L,
                    "zones" to
                        listOf(
                            mapOf(
                                "zone" to "z-a",
                                "candidates" to
                                    listOf(
                                        mapOf(
                                            "serverId" to "lobby-1",
                                            "score" to 88,
                                            "level" to "healthy",
                                            "schedulable" to true,
                                            "onlineCount" to 3,
                                            "maxOnline" to 100,
                                        ),
                                    ),
                            ),
                        ),
                )
            }
        val transport = StatusTransport(200, "body")
        val outcome = BeaconApiClient(transport, codec, settings()).scheduleCandidates(identity())

        val success = assertIs<SchedCandidatesOutcome.Success>(outcome)
        assertEquals(1_700_000_000_000L, success.candidates.generatedAtMs)
        val zone = success.candidates.zones.single()
        assertEquals("z-a", zone.zone)
        val cand = zone.candidates.single()
        assertEquals("lobby-1", cand.serverId)
        assertEquals(88, cand.score)
        assertEquals("healthy", cand.level)
        assertEquals(100, cand.maxOnline)
        val req = transport.lastRequest.get()
        assertTrue(req.url.endsWith("/beacon/v2/agent/schedule/candidates"), "应打 v2 候选端点")
        assertEquals("tk", req.headers[BeaconApiClient.HEADER_TOKEN])
        assertEquals("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", req.headers[BeaconApiClient.HEADER_IDENTITY])
    }

    @Test
    fun `candidates 200 解析可选大厅段`() {
        val codec =
            CapturingCodec {
                mapOf(
                    "generatedAtMs" to 1_700_000_000_000L,
                    "zones" to emptyList<Any>(),
                    "lobby" to
                        mapOf(
                            "clusterId" to 12L,
                            "ready" to true,
                            "candidates" to
                                listOf(
                                    mapOf(
                                        "serverId" to "lobby-1",
                                        "score" to 92,
                                        "level" to "healthy",
                                        "schedulable" to true,
                                        "onlineCount" to 8,
                                        "maxOnline" to 300,
                                    ),
                                ),
                        ),
                )
            }

        val outcome = BeaconApiClient(StatusTransport(200, "body"), codec, settings()).scheduleCandidates(identity())

        val lobby = assertIs<SchedCandidatesOutcome.Success>(outcome).candidates.lobby
        assertEquals(12L, lobby?.clusterId)
        assertTrue(lobby?.ready == true)
        assertEquals("lobby-1", lobby?.candidates?.single()?.serverId)
    }

    @Test
    fun `candidates 非 200 降级为 Failed`() {
        val codec = CapturingCodec { emptyMap<String, Any?>() }
        val outcome = BeaconApiClient(StatusTransport(500), codec, settings()).scheduleCandidates(identity())
        assertIs<SchedCandidatesOutcome.Failed>(outcome)
    }

    @Test
    fun `candidates 解析节点自声明标签并区分标签字段缺失（FR-244）`() {
        val codec =
            CapturingCodec {
                mapOf(
                    "generatedAtMs" to 1_700_000_000_000L,
                    "zones" to
                        listOf(
                            mapOf(
                                "zone" to "z-a",
                                "candidates" to
                                    listOf(
                                        // 有 labels 键：键存在即为"控制面支持该字段"，空 map 是"这台没声明过"。
                                        mapOf("serverId" to "with-labels", "labels" to mapOf("example.zone.a" to "true")),
                                        mapOf("serverId" to "empty-labels", "labels" to emptyMap<String, Any?>()),
                                        // 无 labels 键：旧控制面，**看不到**声明（不是"没有声明"）。
                                        mapOf("serverId" to "legacy"),
                                    ),
                            ),
                        ),
                )
            }

        val zone =
            assertIs<SchedCandidatesOutcome.Success>(
                BeaconApiClient(StatusTransport(200, "body"), codec, settings()).scheduleCandidates(identity()),
            ).candidates.zones.single()

        val byId = zone.candidates.associateBy { it.serverId }
        assertEquals(mapOf("example.zone.a" to "true"), byId.getValue("with-labels").labels)
        assertTrue(byId.getValue("with-labels").labelsPresent)
        assertTrue(byId.getValue("empty-labels").labelsPresent, "有键但空 map 也是「支持该字段」")
        assertTrue(byId.getValue("empty-labels").labels.isEmpty())
        assertTrue(!byId.getValue("legacy").labelsPresent, "缺键 = 旧控制面，看不到声明")
    }

    // ---- decide ----

    @Test
    fun `decide 请求体 camelCase 键与 800ms 读超时`() {
        val codec = CapturingCodec { mapOf("traceId" to "t1", "candidateCount" to 1, "excludedCount" to 0) }
        val transport = StatusTransport(200, "body")
        BeaconApiClient(transport, codec, settings())
            .scheduleDecide(identity(), zone = "z-a", purpose = "lobby-transfer", plugin = "demo")

        val body = encoded(codec)
        assertEquals(setOf("zone", "purpose", "plugin"), body.keys, "decide 请求体键须为 camelCase 且含可选项")
        assertEquals("z-a", body["zone"])
        assertEquals("lobby-transfer", body["purpose"])
        assertEquals("demo", body["plugin"])
        val req = transport.lastRequest.get()
        assertTrue(req.url.endsWith("/beacon/v2/agent/schedule/decide"), "应打 v2 决策端点")
        assertEquals(800L, req.readTimeoutMs, "decide 读超时须为 800ms")
    }

    @Test
    fun `decide 省略空 purpose 与 plugin`() {
        val codec = CapturingCodec { mapOf("traceId" to "t1") }
        BeaconApiClient(StatusTransport(200, "body"), codec, settings())
            .scheduleDecide(identity(), zone = "z-a", purpose = null, plugin = "")
        assertEquals(setOf("zone"), encoded(codec).keys, "空 purpose/plugin 不应拼入请求体")
    }

    @Test
    fun `decide 200 选中候选`() {
        val codec =
            CapturingCodec {
                mapOf(
                    "traceId" to "trace-1",
                    "chosen" to mapOf("serverId" to "lobby-1", "score" to 88),
                    "candidateCount" to 2,
                    "excludedCount" to 1,
                )
            }
        val outcome =
            BeaconApiClient(StatusTransport(200, "body"), codec, settings())
                .scheduleDecide(identity(), "z-a", null, null)
        val decided = assertIs<SchedDecideOutcome.Decided>(outcome)
        assertEquals("trace-1", decided.traceId)
        assertEquals("lobby-1", decided.chosen?.serverId)
        assertEquals(88, decided.chosen?.score)
        assertEquals(2, decided.candidateCount)
        assertEquals(1, decided.excludedCount)
        assertNull(decided.failReason)
    }

    @Test
    fun `decide 200 无候选携 failReason`() {
        val codec =
            CapturingCodec {
                mapOf("traceId" to "trace-2", "chosen" to null, "candidateCount" to 0, "failReason" to "no_candidate")
            }
        val outcome =
            BeaconApiClient(StatusTransport(200, "body"), codec, settings())
                .scheduleDecide(identity(), "z-a", null, null)
        val decided = assertIs<SchedDecideOutcome.Decided>(outcome)
        assertNull(decided.chosen)
        assertEquals("no_candidate", decided.failReason)
    }

    @Test
    fun `decide 状态码映射 404 403 400`() {
        val emptyCodec = CapturingCodec { emptyMap<String, Any?>() }
        val zoneNf = BeaconApiClient(StatusTransport(404), emptyCodec, settings()).scheduleDecide(identity(), "z-a", null, null)
        assertIs<SchedDecideOutcome.ZoneNotFound>(zoneNf)

        val cross = BeaconApiClient(StatusTransport(403), emptyCodec, settings()).scheduleDecide(identity(), "z-a", null, null)
        assertIs<SchedDecideOutcome.CrossNamespace>(cross)

        val rejectCodec = CapturingCodec { mapOf("code" to "INVALID_PARAM") }
        val rejected = BeaconApiClient(StatusTransport(400, "err"), rejectCodec, settings()).scheduleDecide(identity(), "", null, null)
        assertEquals("INVALID_PARAM", assertIs<SchedDecideOutcome.Rejected>(rejected).reason)
    }

    // ---- decide：准入作用域（FR-244） ----

    @Test
    fun `decide 非空准入作用域随请求体下发`() {
        val codec = CapturingCodec { mapOf("traceId" to "t1") }
        BeaconApiClient(StatusTransport(200, "body"), codec, settings())
            .scheduleDecide(
                identity(),
                zone = "z-a",
                purpose = null,
                plugin = null,
                admissionScope = listOf(mapOf("example.zone.lobby-a" to "true")),
            )

        val body = encoded(codec)
        assertEquals(setOf("zone", "admissionScope"), body.keys, "非空作用域时应下发 admissionScope 键")
        assertEquals(
            listOf(mapOf("example.zone.lobby-a" to "true")),
            body["admissionScope"],
            "作用域按「备选列表」下发（备选之间 OR）",
        )
    }

    @Test
    fun `decide 空准入作用域不下发该键（向后兼容）`() {
        val codec = CapturingCodec { mapOf("traceId" to "t1") }
        BeaconApiClient(StatusTransport(200, "body"), codec, settings())
            .scheduleDecide(identity(), "z-a", null, null, admissionScope = emptyList())
        assertEquals(setOf("zone"), encoded(codec).keys, "空作用域不应把 admissionScope 拼进请求体")
    }

    @Test
    fun `decide 503 映射为准入判不了而不是连接失败`() {
        val codec = CapturingCodec { mapOf("code" to "admission_unavailable") }
        val outcome =
            BeaconApiClient(StatusTransport(503, "err"), codec, settings())
                .scheduleDecide(identity(), "z-a", null, null, admissionScope = listOf(mapOf("k" to "v")))
        assertEquals(
            "admission_unavailable",
            assertIs<SchedDecideOutcome.AdmissionUnavailable>(outcome).reason,
            "503 是「判不了」（当前状态），必须与 Failed（连不上）分开",
        )
    }

    // ---- report-local ----

    @Test
    fun `report-local 请求体 camelCase 键与 202 受理`() {
        val codec = CapturingCodec { mapOf("accepted" to 1, "deduplicated" to 0) }
        val transport = StatusTransport(202, "body")
        val outcome =
            BeaconApiClient(transport, codec, settings()).reportLocalDecisions(
                identity(),
                listOf(
                    LocalDecisionReport(
                        localTraceId = "local-1",
                        tsMs = 123L,
                        zone = "z-a",
                        plugin = null,
                        purpose = "降级演练",
                        candidateCount = 1,
                        excluded = listOf(ExcludedRef("lobby-2", "unhealthy")),
                        chosenServerId = "lobby-1",
                        failReason = null,
                    ),
                ),
            )

        val accepted = assertIs<SchedReportLocalOutcome.Accepted>(outcome)
        assertEquals(1, accepted.accepted)
        val decision = firstDecision(encoded(codec))
        assertEquals(
            setOf("localTraceId", "tsMs", "zone", "plugin", "purpose", "candidateCount", "excluded", "chosenServerId", "failReason"),
            decision.keys,
            "decisions 元素键须为 camelCase 全集",
        )
        assertEquals("local-1", decision["localTraceId"])
        assertEquals("lobby-1", decision["chosenServerId"])
        assertEquals("", decision["plugin"], "空 plugin 以空串占位")
        assertEquals("", decision["failReason"], "空 failReason 以空串占位")
        val excluded = firstExcluded(decision)
        assertEquals("lobby-2", excluded["serverId"])
        assertEquals("unhealthy", excluded["reason"])
        assertTrue(transport.lastRequest.get().url.endsWith("/beacon/v2/agent/schedule/report-local"))
    }

    @Test
    fun `report-local 400 超限携原因`() {
        val codec = CapturingCodec { mapOf("code" to "INVALID_PARAM") }
        val outcome = BeaconApiClient(StatusTransport(400, "err"), codec, settings()).reportLocalDecisions(identity(), emptyList())
        assertEquals("INVALID_PARAM", assertIs<SchedReportLocalOutcome.Rejected>(outcome).reason)
    }

    // ---- 指标上报 self 解析 ----

    @Test
    fun `指标上报 202 解析 self 健康段`() {
        val codec =
            CapturingCodec {
                mapOf(
                    "accepted" to 1,
                    "deduplicated" to 0,
                    "self" to
                        mapOf(
                            "score" to 72,
                            "level" to "degraded",
                            "schedulable" to true,
                            "reasons" to listOf<Any?>(),
                        ),
                )
            }
        val outcome =
            BeaconApiClient(StatusTransport(202), codec, settings())
                .reportMetricsBatch(identity(), MetricKind.BACKEND, 1L, 0L, listOf(backendBatch()))
        val accepted = assertIs<MetricsReportOutcome.Accepted>(outcome)
        val self = accepted.self
        assertEquals(72, self?.score)
        assertEquals("degraded", self?.level)
        assertEquals(true, self?.schedulable)
    }

    @Test
    fun `指标上报 202 缺 self 段返回 null`() {
        val codec = CapturingCodec { mapOf("accepted" to 1, "deduplicated" to 0) }
        val outcome =
            BeaconApiClient(StatusTransport(202), codec, settings())
                .reportMetricsBatch(identity(), MetricKind.BACKEND, 1L, 0L, listOf(backendBatch()))
        assertNull(assertIs<MetricsReportOutcome.Accepted>(outcome).self)
    }

    private fun backendBatch() =
        MetricBatch(
            bucketStartMs = 5000L,
            sampleCount = 5,
            load = LoadAgg(cpuPctAvg = 40.0, cpuPctMax = 55.0, memUsedMbAvg = 512.0, memMaxMb = 2048),
            reportRttMs = 12,
            payload = BackendBatch(tpsAvg = 19.5, tpsMin = 18.0, onlineAvg = 30, onlineMax = 40, maxOnline = 100),
        )
}
