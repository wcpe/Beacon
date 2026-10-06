package top.wcpe.beacon.agent.core.scheduling

import top.wcpe.beacon.agent.api.AdmissionScope
import top.wcpe.beacon.agent.api.DecisionSource
import top.wcpe.beacon.agent.api.ScheduleState
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.io.File
import java.nio.file.Files
import java.util.concurrent.atomic.AtomicReference
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * 准入作用域与三态结论的单测（FR-244）。
 *
 * 锁四件事：
 * ① 作用域随 decide 请求体下发（空作用域不下发，请求体逐字不变）；
 * ② 降级路径**按作用域改变决策结果**——不是"选完再校验"，而是分低的满足者胜过分高的不满足者；
 * ③ 三态可分：空（稳定）/ 不可用（当前状态）绝不互相冒充，两种"判不了"（无快照 / 快照缺标签字段）
 *    都归不可用；
 * ④ 候选读侧的收窄与"看不到判据"的处置（抛，而不是返回空表）。
 */
class SchedulingAdmissionScopeTest {
    private val zone = "z-a"

    /** 我们要的准入条件：候选节点必须声明过它服务这个区（键名是本仓之外的接入方约定）。 */
    private val wantLobby = AdmissionScope.of("example.zone.lobby-a", "true")

    private val tempDir: File = Files.createTempDirectory("sched-scope").toFile()
    private val adapter = ManualSchedAdapter(tempDir)
    private val cache = SchedulingCache(now = { 10_000L })
    private val reportQueue = LocalDecisionReportQueue()
    private val selfHealthHolder = SelfHealthHolder(now = { 10_000L })

    /** 只路由两个端点；decide 会带请求体，故记下最后一次编码的树。 */
    private class ScopeTransport : HttpTransport {
        @Volatile var down = false

        @Volatile var decideStatus = 200

        override fun execute(request: HttpRequest): HttpResponse {
            if (down) {
                throw RuntimeException("模拟控制面不可达")
            }
            return if (request.url.contains("/schedule/decide")) {
                HttpResponse(decideStatus, DECIDE)
            } else {
                HttpResponse(200, CANDIDATES)
            }
        }

        companion object {
            const val DECIDE = "decide-body"
            const val CANDIDATES = "candidates-body"
        }
    }

    private class ScopeCodec(
        private val decideBody: Map<String, Any?>,
        private val candidatesBody: Map<String, Any?> = emptyMap(),
    ) : JsonCodec {
        val lastEncoded = AtomicReference<Any?>(null)

        override fun encode(value: Any?): String {
            lastEncoded.set(value)
            return "encoded"
        }

        override fun decode(json: String): Any? =
            when (json) {
                ScopeTransport.DECIDE -> decideBody
                ScopeTransport.CANDIDATES -> candidatesBody
                else -> emptyMap<String, Any?>()
            }
    }

    private fun newView(
        transport: ScopeTransport,
        codec: ScopeCodec,
    ) = SchedulingView(
        BeaconApiClient(transport, codec, schedSettings()),
        schedIdentity(),
        adapter,
        cache,
        reportQueue,
        selfHealthHolder,
        now = { 10_000L },
    )

    /** 两台候选：高分那台**不服务**本区（未被作用域要求），低分那台服务。 */
    private fun populateTwoCandidates() {
        cache.set(
            snapshotOf(
                zone,
                listOf(
                    candidateEntry("game1201", 90, labels = mapOf("example.zone.other-a" to "true"), labelsPresent = true),
                    candidateEntry("game1202", 80, labels = mapOf("example.zone.lobby-a" to "true"), labelsPresent = true),
                ),
                savedAtMs = 10_000L,
            ),
            live = true,
        )
    }

    @Suppress("UNCHECKED_CAST")
    private fun encodedBody(codec: ScopeCodec): Map<String, Any?> = codec.lastEncoded.get() as Map<String, Any?>

    private fun chosenDecideBody() =
        mapOf<String, Any?>(
            "traceId" to "srv-trace-1",
            "chosen" to mapOf("serverId" to "game1202", "score" to 80),
            "candidateCount" to 2,
            "excludedCount" to 0,
        )

    // ---- ① 作用域真的下发了 ----

    @Test
    fun `非空作用域随 decide 请求体下发`() {
        populateTwoCandidates()
        val codec = ScopeCodec(chosenDecideBody())
        newView(ScopeTransport(), codec).acquireCandidate(zone, null, wantLobby).get()

        val body = encodedBody(codec)
        assertEquals(
            listOf(mapOf("example.zone.lobby-a" to "true")),
            body["admissionScope"],
            "作用域必须随请求体到控制面，否则收窄发生在本地、证不到「决策阶段收窄」",
        )
    }

    @Test
    fun `空作用域不下发 admissionScope 键（向后兼容）`() {
        populateTwoCandidates()
        val codec = ScopeCodec(chosenDecideBody())
        newView(ScopeTransport(), codec).acquireCandidate(zone, null, AdmissionScope.empty()).get()

        val body = encodedBody(codec)
        assertTrue(!body.containsKey("admissionScope"), "空作用域时请求体应与改动前逐字一致")
    }

    // ---- ② 降级路径按作用域改变结果 ----

    @Test
    fun `降级路径按作用域收窄：分低的满足者胜过分高的不满足者`() {
        populateTwoCandidates()
        val transport = ScopeTransport().apply { down = true }
        val result = newView(transport, ScopeCodec(chosenDecideBody())).acquireCandidate(zone, null, wantLobby).get()

        assertEquals(DecisionSource.LOCAL_FALLBACK, result.source())
        assertEquals(ScheduleState.CHOSEN, result.state())
        assertEquals(
            "game1202",
            result.chosen()?.serverId(),
            "选出的必须是满足作用域的那台（80 分）；若不满足作用域的 90 分那台胜出，说明作用域没进决策",
        )
        assertEquals(80, result.chosen()?.score())
        // 选中的候选视图也带出它自己的标签（同一真源）。
        assertEquals(mapOf("example.zone.lobby-a" to "true"), result.chosen()?.labels())
    }

    @Test
    fun `降级路径不带作用域时仍是最高分（作用域为空逐位等价）`() {
        populateTwoCandidates()
        val transport = ScopeTransport().apply { down = true }
        val result = newView(transport, ScopeCodec(chosenDecideBody())).acquireCandidate(zone).get()
        assertEquals("game1201", result.chosen()?.serverId(), "空作用域下最高分（90）照旧胜出")
    }

    @Test
    fun `降级路径全被作用域滤掉时归为空结果且原因可读`() {
        populateTwoCandidates()
        val transport = ScopeTransport().apply { down = true }
        val nobody = AdmissionScope.of("example.zone.nobody", "true")
        val result = newView(transport, ScopeCodec(chosenDecideBody())).acquireCandidate(zone, null, nobody).get()

        assertEquals(ScheduleState.NO_CANDIDATE, result.state(), "作用域把候选全滤掉是稳定事实，不是不可用")
        assertEquals("no_candidate_in_scope", result.failReason())
        assertEquals(
            2,
            result.admissionExcludedCount(),
            "降级路径也要照实回报被作用域排除的台数：与控制面路径同一口径，>0 才说明确实收窄过",
        )
        assertNull(result.chosen())
        val queued = reportQueue.drain().single()
        assertEquals(2, queued.candidateCount)
        assertEquals(
            listOf("game1201", "game1202"),
            queued.excluded.map { it.serverId },
            "被作用域排除的节点要如实入补报（否则降级期的收窄没有痕迹）",
        )
        assertEquals("admission_scope_mismatch", queued.excluded.first().reason)
        assertEquals("no_candidate_in_scope", queued.failReason)
    }

    // ---- ③ 三态：不可用绝不被印成空 ----

    @Test
    fun `降级路径没有快照时归为不可用而不是没有候选`() {
        val transport = ScopeTransport().apply { down = true }
        val result = newView(transport, ScopeCodec(chosenDecideBody())).acquireCandidate(zone).get()

        assertEquals(ScheduleState.UNAVAILABLE, result.state())
        assertEquals("unavailable", result.failReason())
        assertNull(result.chosen())
        assertTrue(
            adapter.warns.any { it.contains("unavailable") },
            "判不了这件事必须留痕，不能只体现为一个空结果",
        )
    }

    @Test
    fun `降级路径快照缺标签字段且作用域非空时归为不可用`() {
        // 旧控制面：候选里没有 labels 键（labelsPresent=false）。
        cache.set(
            snapshotOf(zone, listOf(candidateEntry("game1201", 90, labelsPresent = false)), savedAtMs = 10_000L),
            live = true,
        )
        val transport = ScopeTransport().apply { down = true }
        val result = newView(transport, ScopeCodec(chosenDecideBody())).acquireCandidate(zone, null, wantLobby).get()

        assertEquals(ScheduleState.UNAVAILABLE, result.state(), "判据看不到时不能报「没有符合条件的候选」")
        assertEquals("admission_scope_unavailable", result.failReason())
    }

    @Test
    fun `控制面判不了作用域（503）时降级并按作用域选`() {
        populateTwoCandidates()
        val transport = ScopeTransport().apply { decideStatus = 503 }
        val codec = ScopeCodec(mapOf("code" to "admission_unavailable"))
        val result = newView(transport, codec).acquireCandidate(zone, null, wantLobby).get()

        assertEquals(DecisionSource.LOCAL_FALLBACK, result.source())
        assertEquals(ScheduleState.CHOSEN, result.state())
        assertEquals("game1202", result.chosen()?.serverId())
    }

    @Test
    fun `控制面回 no_candidate_in_scope 时归为空结果（稳定事实）`() {
        populateTwoCandidates()
        val codec =
            ScopeCodec(
                mapOf(
                    "traceId" to "srv-trace-2",
                    "chosen" to null,
                    "candidateCount" to 2,
                    "excludedCount" to 2,
                    "failReason" to "no_candidate_in_scope",
                ),
            )
        val result = newView(ScopeTransport(), codec).acquireCandidate(zone, null, wantLobby).get()

        assertEquals(DecisionSource.CONTROL_PLANE, result.source())
        assertEquals(ScheduleState.NO_CANDIDATE, result.state())
        assertEquals("no_candidate_in_scope", result.failReason())
        assertEquals(0, reportQueue.size(), "控制面权威结论不入补报队列")
    }

    @Test
    fun `控制面回无候选（无作用域）时仍是空结果`() {
        populateTwoCandidates()
        val codec =
            ScopeCodec(
                mapOf("traceId" to "t", "chosen" to null, "candidateCount" to 0, "failReason" to "no_candidate"),
            )
        val result = newView(ScopeTransport(), codec).acquireCandidate(zone).get()
        assertEquals(ScheduleState.NO_CANDIDATE, result.state())
        assertEquals("no_candidate", result.failReason())
    }

    @Test
    fun `降级路径按 OR 备选收窄：不限制服务范围的节点照样进候选`() {
        // 「不限制 ∨ 明确服务该区」——调用方真值表里两档并列的受理形态。
        val orScope =
            AdmissionScope.anyOf(
                AdmissionScope.of("example.zones", ""),
                AdmissionScope.of("example.zone.lobby-a", "true"),
            )
        cache.set(
            snapshotOf(
                zone,
                listOf(
                    candidateEntry("unrestricted", 90, labels = mapOf("example.zones" to ""), labelsPresent = true),
                    candidateEntry("serves-lobby", 70, labels = mapOf("example.zone.lobby-a" to "true"), labelsPresent = true),
                    candidateEntry("serves-other", 99, labels = mapOf("example.zone.other-a" to "true"), labelsPresent = true),
                ),
                savedAtMs = 10_000L,
            ),
            live = true,
        )
        val transport = ScopeTransport().apply { down = true }
        val result = newView(transport, ScopeCodec(chosenDecideBody())).acquireCandidate(zone, null, orScope).get()

        assertEquals("unrestricted", result.chosen()?.serverId(), "不限制的节点（90 分）应胜出")
        val queued = reportQueue.drain().single()
        assertEquals(listOf("serves-other"), queued.excluded.map { it.serverId }, "只服务别的区的那台应被剔除")
    }

    @Test
    fun `控制面回报的作用域排除台数原样带出`() {
        populateTwoCandidates()
        val codec =
            ScopeCodec(
                mapOf(
                    "traceId" to "srv-trace-3",
                    "chosen" to mapOf("serverId" to "game1202", "score" to 80),
                    "candidateCount" to 2,
                    "excludedCount" to 1,
                    "admissionExcludedCount" to 1,
                ),
            )
        val result = newView(ScopeTransport(), codec).acquireCandidate(zone, null, wantLobby).get()

        assertEquals(ScheduleState.CHOSEN, result.state())
        assertEquals(
            1,
            result.admissionExcludedCount(),
            "「决策阶段确实收窄了」这件事必须在来源侧可读（否则只能去猜对端有没有按作用域收窄）",
        )
    }

    @Test
    fun `旧控制面缺 admissionExcludedCount 键时按 0 解析`() {
        populateTwoCandidates()
        val result = newView(ScopeTransport(), ScopeCodec(chosenDecideBody())).acquireCandidate(zone).get()
        assertEquals(0, result.admissionExcludedCount(), "缺键 = 没被收窄（向后兼容，不臆造台数）")
    }

    // ---- ④ 候选读侧的收窄 ----

    @Test
    fun `候选查询按作用域只留满足者`() {
        populateTwoCandidates()
        val view = newView(ScopeTransport(), ScopeCodec(chosenDecideBody()))
        assertEquals(listOf("game1201", "game1202"), view.candidatesInZone(zone).map { it.serverId() })
        assertEquals(
            listOf("game1202"),
            view.candidatesInZone(zone, wantLobby).map { it.serverId() },
            "带作用域的候选读侧与决策侧是同一套条件",
        )
        assertEquals(2, view.candidatesInZone(zone, AdmissionScope.empty()).size, "空作用域 = 全量")
    }

    @Test
    fun `候选快照缺标签字段时带作用域的查询抛而不是返回空表`() {
        cache.set(
            snapshotOf(zone, listOf(candidateEntry("game1201", 90, labelsPresent = false)), savedAtMs = 10_000L),
            live = true,
        )
        val view = newView(ScopeTransport(), ScopeCodec(chosenDecideBody()))
        val failure = assertFailsWith<IllegalStateException> { view.candidatesInZone(zone, wantLobby) }
        assertTrue(
            failure.message!!.contains("labels"),
            "报因要指名是标签字段缺失（判据看不到），否则会被读成「没有候选」",
        )
        // 不带作用域时照旧可用（向后兼容）。
        assertEquals(listOf("game1201"), view.candidatesInZone(zone).map { it.serverId() })
    }

    @Test
    fun `该区一台候选都没有时带作用域的查询返回空表而不是抛`() {
        // 「没有候选」与「判据看不到」是两件事：区内空表时不去猜后者（依赖控制面只下发有可调度候选的区）。
        cache.set(snapshotOf("z-empty", emptyList(), savedAtMs = 10_000L), live = true)
        val view = newView(ScopeTransport(), ScopeCodec(chosenDecideBody()))

        assertEquals(emptyList(), view.candidatesInZone("z-empty", wantLobby).map { it.serverId() })
    }

    // ---- ⑤ 落盘 → 重启恢复后的口径保真 ----

    @Test
    fun `落盘恢复后仍能按作用域收窄（标签随快照一起回来）`() {
        val store = SchedulingSnapshotStore(File(tempDir, "with-labels.json"), RoundTripCodec())
        store.write(
            snapshotOf(
                zone,
                listOf(
                    candidateEntry("game1201", 90, labels = mapOf("example.zone.other-a" to "true"), labelsPresent = true),
                    candidateEntry("game1202", 80, labels = mapOf("example.zone.lobby-a" to "true"), labelsPresent = true),
                ),
                savedAtMs = 10_000L,
            ),
        )
        // 重启后：缓存只可能来自落盘（live = false），标签必须还在，否则作用域判定会凭空变"不可用"。
        cache.set(store.read()!!, live = false)
        val transport = ScopeTransport().apply { down = true }
        val result = newView(transport, ScopeCodec(chosenDecideBody())).acquireCandidate(zone, null, wantLobby).get()

        assertEquals(ScheduleState.CHOSEN, result.state())
        assertEquals("game1202", result.chosen()?.serverId())
        assertEquals(1, result.admissionExcludedCount(), "恢复后的快照照样照实回报被收窄的台数")
        assertEquals(mapOf("example.zone.lobby-a" to "true"), result.chosen()?.labels())
    }

    @Test
    fun `旧格式落盘恢复的快照仍读成看不到（不自造标签）`() {
        // 落盘时就只有既有字段、没有 labels 键（重装 / 升级前的快照）。
        val store = SchedulingSnapshotStore(File(tempDir, "legacy.json"), RoundTripCodec())
        store.write(snapshotOf(zone, listOf(candidateEntry("game1201", 90, labelsPresent = false)), savedAtMs = 10_000L))
        cache.set(store.read()!!, live = false)
        val transport = ScopeTransport().apply { down = true }
        val result = newView(transport, ScopeCodec(chosenDecideBody())).acquireCandidate(zone, null, wantLobby).get()

        assertEquals(ScheduleState.UNAVAILABLE, result.state(), "判据看不到时不能报「没有符合条件的候选」")
        assertEquals("admission_scope_unavailable", result.failReason())
    }
}
