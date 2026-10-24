package top.wcpe.beacon.agent.core.scheduling

import top.wcpe.beacon.agent.api.AdmissionScope
import top.wcpe.beacon.agent.api.BeaconScheduling
import top.wcpe.beacon.agent.api.CandidateView
import top.wcpe.beacon.agent.api.DataSource
import top.wcpe.beacon.agent.api.DataSourceState
import top.wcpe.beacon.agent.api.DecisionSource
import top.wcpe.beacon.agent.api.HealthLevel
import top.wcpe.beacon.agent.api.HealthView
import top.wcpe.beacon.agent.api.ScheduleResult
import top.wcpe.beacon.agent.api.ScheduleState
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.client.CandidateEntry
import top.wcpe.beacon.agent.core.client.LobbyCandidates
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.io.File
import java.nio.file.Files
import java.util.concurrent.CompletableFuture
import java.util.concurrent.atomic.AtomicReference
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * 全局大厅池调度的单测（FR-281）。
 *
 * 锁六件事：
 * ① 读不出大厅池时**抛**而不是返回空表（"看不到"不得冒充"池里没有机器"）；模型在而池空才是空表；
 * ② 候选读侧与决策侧的收窄同一口径（空作用域不收窄、非空只留满足者）；
 * ③ 大厅档的下发真的走 `scope=lobby` 且**不带** zone（否则控制面按小区查，结论口径完全不同）；
 * ④ 三态可分：控制面 4xx 是"没问成"（不可用），绝不是"池里没有候选"；连接级失败降级仍出结果；
 * ⑤ 大厅成员不属于任何小区：选中与读取的视图 `zone()` 都是**空串**，`healthOf` 也要覆盖池成员；
 * ⑥ 接口 `default` 缺省实现是"能力缺失"最响的那一档（抛 UnsupportedOperationException）。
 */
class SchedulingLobbyTest {
    private val tempDir: File = Files.createTempDirectory("sched-lobby").toFile()
    private val adapter = ManualSchedAdapter(tempDir)
    private val cache = SchedulingCache(now = { 10_000L })
    private val reportQueue = LocalDecisionReportQueue()
    private val selfHealthHolder = SelfHealthHolder(now = { 10_000L })

    /** 我们要的准入条件：候选节点必须声明过它服务这个大厅（键名是本仓之外的接入方约定）。 */
    private val wantLobby = AdmissionScope.of("example.zone.lobby-a", "true")

    /** 只路由 decide / candidates 两端点；decide 带请求体，故记下最后一次编码的树。 */
    private class LobbyTransport : HttpTransport {
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

    private class LobbyCodec(
        private val decideBody: Map<String, Any?>,
    ) : JsonCodec {
        val lastEncoded = AtomicReference<Any?>(null)

        override fun encode(value: Any?): String {
            lastEncoded.set(value)
            return "encoded"
        }

        override fun decode(json: String): Any? =
            when (json) {
                LobbyTransport.DECIDE -> decideBody
                else -> emptyMap<String, Any?>()
            }
    }

    private fun newView(
        transport: LobbyTransport = LobbyTransport(),
        codec: LobbyCodec = LobbyCodec(chosenBody("lobby-1", 92)),
    ) = SchedulingView(
        BeaconApiClient(transport, codec, schedSettings()),
        schedIdentity(),
        adapter,
        cache,
        reportQueue,
        selfHealthHolder,
        now = { 10_000L },
    )

    private fun chosenBody(
        serverId: String,
        score: Int,
    ) = mapOf<String, Any?>(
        "traceId" to "srv-lobby-trace",
        "chosen" to mapOf("serverId" to serverId, "score" to score),
        "candidateCount" to 2,
        "excludedCount" to 0,
    )

    @Suppress("UNCHECKED_CAST")
    private fun encodedBody(codec: LobbyCodec): Map<String, Any?> = codec.lastEncoded.get() as Map<String, Any?>

    /** 一帧带大厅段的快照（zones 可空：大厅成员本来就不属于任何小区）。 */
    private fun snapshotWithLobby(
        lobbyCandidates: List<CandidateEntry>,
        zones: Map<String, List<CandidateEntry>> = emptyMap(),
    ): CandidateSnapshot = CandidateSnapshot(1_000L, 10_000L, zones, LobbyCandidates(7L, lobbyCandidates.isNotEmpty(), lobbyCandidates))

    /** 三台池成员：一台不限制服务范围、一台服务本大厅、一台只服务别处。 */
    private fun populateLobby() {
        cache.set(
            snapshotWithLobby(
                listOf(
                    candidateEntry("lobby-free", 90, online = 5, labels = mapOf("example.zones" to ""), labelsPresent = true),
                    candidateEntry("lobby-a", 70, online = 8, labels = mapOf("example.zone.lobby-a" to "true"), labelsPresent = true),
                    candidateEntry("lobby-other", 99, labels = mapOf("example.zone.other" to "true"), labelsPresent = true),
                ),
            ),
            live = true,
        )
    }

    // ---- ① 读不出大厅池时抛，模型在而池空才是空表 ----

    @Test
    fun `本帧没有大厅模型时读取抛而不是返回空表`() {
        // 旧控制面：候选帧里压根没有 lobby 段（这里连小区段都给了，证明抛出只因大厅模型缺失）。
        cache.set(
            snapshotOf("z-a", listOf(candidateEntry("game1201", 90, labelsPresent = true)), savedAtMs = 10_000L),
            live = true,
        )
        val view = newView()

        val failure = assertFailsWith<IllegalStateException> { view.candidatesInLobby() }
        assertTrue(
            failure.message!!.contains("大厅"),
            "报因要指名是「没有大厅模型」（判据看不到），否则会被读成「池里没有机器」",
        )
        // 小区档不受影响（两段各自独立）。
        assertEquals(listOf("game1201"), view.candidatesInZone("z-a").map { it.serverId() })
    }

    @Test
    fun `大厅模型在但池里没有成员时返回空表`() {
        // 本 namespace 还没配大厅集群 / 集群里当前没有可调度成员：这是稳定事实，照常返回空表。
        cache.set(snapshotWithLobby(emptyList()), live = true)

        assertEquals(emptyList(), newView().candidatesInLobby().map { it.serverId() })
    }

    @Test
    fun `带作用域的读取在无大厅模型时同样抛而不是返回空表`() {
        // 带参重载与无参重载共用同一处判据，但契约对两条都成立，故单独锁一遍：
        // 空表会把"看不到大厅池"冒充成"池里没有机器"，无论调用方是否带作用域。
        cache.set(
            snapshotOf("z-a", listOf(candidateEntry("game1201", 90, labelsPresent = true)), savedAtMs = 10_000L),
            live = true,
        )

        assertFailsWith<IllegalStateException> { newView().candidatesInLobby(wantLobby) }
    }

    @Test
    fun `带作用域的读取在大厅模型存在但池为空时返回空表`() {
        // 池空是稳定事实：模型在，就没有「看不到」这回事，带不带作用域都该返回空表而不是抛。
        cache.set(snapshotWithLobby(emptyList()), live = true)

        assertEquals(emptyList(), newView().candidatesInLobby(wantLobby).map { it.serverId() })
    }

    // ---- ② 读侧按作用域收窄，且视图 zone 为空串 ----

    @Test
    fun `大厅候选按作用域收窄：空作用域不收窄而非空只留满足者`() {
        populateLobby()
        val view = newView()

        assertEquals(
            listOf("lobby-free", "lobby-a", "lobby-other"),
            view.candidatesInLobby().map { it.serverId() },
            "空作用域不排除任何候选（与不带作用域的重载逐位等价）",
        )
        assertEquals(
            listOf("lobby-a"),
            view.candidatesInLobby(wantLobby).map { it.serverId() },
            "非空作用域只留满足者：读侧与决策侧是同一套条件",
        )
        assertTrue(
            view.candidatesInLobby(wantLobby).all { it.zone() == "" },
            "大厅成员不属于任何小区，视图 zone 必须是空串（不是未知小区）",
        )
    }

    @Test
    fun `大厅候选缺自声明标签字段时带作用域的读取抛而不是返回空表`() {
        // 旧控制面 / 未装配标签真源：候选里没有 labels 键（labelsPresent=false）。
        cache.set(snapshotWithLobby(listOf(candidateEntry("lobby-1", 90, labelsPresent = false))), live = true)
        val view = newView()

        val failure = assertFailsWith<IllegalStateException> { view.candidatesInLobby(wantLobby) }
        assertTrue(
            failure.message!!.contains("labels"),
            "报因要指名是标签字段缺失（判据看不到），否则会被读成「没有满足条件的候选」",
        )
        // 不带作用域时不碰判据，照旧返回全量（与 zone 档同一口径）。
        assertEquals(listOf("lobby-1"), view.candidatesInLobby().map { it.serverId() })
    }

    @Test
    fun `作用域把大厅池成员全滤掉时读取返回空表`() {
        // 与上一条严格区分：标签字段齐全=判据看得见，只是没有一个满足作用域。
        // 那是稳定事实（收窄后确实没有），必须返回空表，不能抛。
        populateLobby()
        val nobody = AdmissionScope.of("example.zone.nobody", "true")

        assertEquals(
            emptyList(),
            newView().candidatesInLobby(nobody).map { it.serverId() },
            "判据看得见但无人满足作用域，是稳定事实；与「判据看不到」的抛出处境必须分开",
        )
    }

    // ---- ③ 下发口径：scope=lobby 且不带 zone ----

    @Test
    fun `大厅决策下发 scope等于lobby 且不带 zone 键`() {
        populateLobby()
        val codec = LobbyCodec(chosenBody("lobby-a", 70))
        newView(codec = codec).acquireCandidateInLobby("lobby-transfer").get()

        val body = encodedBody(codec)
        assertEquals("lobby", body["scope"], "不下发 scope 时控制面会按缺省的 zone 档处理，结论口径完全不同")
        assertTrue(
            !body.containsKey("zone"),
            "大厅档不能带 zone：控制面要求该档 zone 必须为空，带一个构造出来的 zone 名会得到 zone_not_found",
        )
        assertEquals("lobby-transfer", body["purpose"], "用途说明照旧随请求入库供排查")
    }

    @Test
    fun `小区档决策请求体不含 scope 键（与改动前逐字一致）`() {
        // 兼容承诺：老控制面对未知键虽不报错，但"逐字不变"是更硬的保证。
        // 缺省形参 scope=null 时不发该键，避免把小区档误标成大厅档。
        populateLobby()
        val codec = LobbyCodec(chosenBody("lobby-a", 70))
        newView(codec = codec).acquireCandidate("z-a", "zone-transfer", null).get()

        val body = encodedBody(codec)
        assertTrue(
            !body.containsKey("scope"),
            "小区档不得下发 scope 键：老控制面按缺省 zone 档处理，多一个键等于改变协议形状",
        )
        assertEquals("z-a", body["zone"], "小区档照旧下发 zone")
    }

    // ---- ④ 四态结论：4xx 是「没问成」，不是「池里没有候选」 ----

    @Test
    fun `控制面回 400 时归为不可用而不是没有候选`() {
        populateLobby()
        val transport = LobbyTransport().apply { decideStatus = 400 }
        val codec = LobbyCodec(mapOf("code" to "INVALID_PARAM"))
        val result = newView(transport, codec).acquireCandidateInLobby("lobby-transfer").get()

        assertEquals(ScheduleState.UNAVAILABLE, result.state(), "4xx 是「没问成」（当前状态、可重试），不是「确实没有候选」")
        assertEquals(DecisionSource.CONTROL_PLANE, result.source())
        assertEquals("INVALID_PARAM", result.failReason(), "对端码原样带出，便于定位是对端不认识大厅作用域还是参数被拒")
        assertNull(result.chosen())
        assertEquals(0, reportQueue.size(), "没问成不入补报队列（与小区档的权威失败同处置）")
    }

    @Test
    fun `控制面回 404 时同样归为不可用（旧控制面把无 zone 判成小区不存在）`() {
        populateLobby()
        val transport = LobbyTransport().apply { decideStatus = 404 }
        val result = newView(transport).acquireCandidateInLobby("lobby-transfer").get()

        assertEquals(ScheduleState.UNAVAILABLE, result.state(), "大厅档的「问到了」只有 200 一条路，404 只能是没问成")
        assertEquals("zone_not_found", result.failReason())
    }

    @Test
    fun `控制面回无候选（池空）时归为空结果`() {
        cache.set(snapshotWithLobby(emptyList()), live = true)
        val codec =
            LobbyCodec(
                mapOf("traceId" to "t-lobby", "chosen" to null, "candidateCount" to 0, "failReason" to "no_candidate"),
            )
        val result = newView(codec = codec).acquireCandidateInLobby(null).get()

        assertEquals(ScheduleState.NO_CANDIDATE, result.state(), "池空是稳定事实（问到了，确实没有成员）")
        assertEquals("no_candidate", result.failReason())
        assertNull(result.chosen())
    }

    // ---- ⑤ 选中视图与 healthOf 的口径 ----

    @Test
    fun `大厅决策选中时视图 zone 为空串并从大厅段补全展示字段`() {
        populateLobby()
        val codec = LobbyCodec(chosenBody("lobby-a", 70))
        val result = newView(codec = codec).acquireCandidateInLobby("lobby-transfer").get()

        assertEquals(ScheduleState.CHOSEN, result.state())
        val chosen = result.chosen()!!
        assertEquals("lobby-a", chosen.serverId())
        assertEquals("", chosen.zone(), "大厅成员不属于任何小区：zone() 必须是空串")
        assertEquals(70, chosen.score())
        // level / online / max 从本地快照的**大厅段**补全（不能去小区段找）。
        assertEquals(HealthLevel.HEALTHY, chosen.level())
        assertEquals(8, chosen.onlineCount())
        assertEquals(100, chosen.maxOnline())
        assertEquals("srv-lobby-trace", result.traceId(), "以服务端 traceId 为准")
    }

    @Test
    fun `healthOf 覆盖全局大厅池的成员`() {
        populateLobby()
        val health = newView().healthOf("lobby-a")

        assertEquals(70, health?.score(), "池成员没有 zone，只按小区找会让它的 healthOf 恒为 null")
        assertEquals(HealthLevel.HEALTHY, health?.level())
        assertNull(newView().healthOf("nope"))
    }

    // ---- ⑥ fail-static 降级与「看不到」的三态 ----

    @Test
    fun `控制面不可达时用大厅段本地降级仍能出结果`() {
        populateLobby()
        val transport = LobbyTransport().apply { down = true }
        val future = newView(transport).acquireCandidateInLobby("lobby-transfer")
        val result = future.get()

        assertTrue(!future.isCompletedExceptionally, "fail-static：future 绝不异常完成")
        assertEquals(DecisionSource.LOCAL_FALLBACK, result.source())
        assertEquals(ScheduleState.CHOSEN, result.state())
        assertEquals("lobby-other", result.chosen()?.serverId(), "本地 highest_score 应选分最高者（99）")
        assertEquals("", result.chosen()?.zone())
        // 大厅决策没有 zone，而 report-local 的校验要求 zone 非空（空 zone 会 400 并堵住后续整批补报），
        // 故大厅档不入补报队列——这条断言锁住「不要为了补报把队列毒化」。
        assertEquals(0, reportQueue.size(), "大厅降级决策不得入补报队列（空 zone 会让整批 400 并堵住后续补报）")
        assertTrue(
            adapter.warns.any { it.contains("不") && it.contains("补报") },
            "不入补报这件事要留痕，不能只体现为一个没被填的队列",
        )
    }

    @Test
    fun `控制面不可达且本帧没有大厅模型时归为不可用`() {
        cache.set(
            snapshotOf("z-a", listOf(candidateEntry("game1201", 90, labelsPresent = true)), savedAtMs = 10_000L),
            live = true,
        )
        val transport = LobbyTransport().apply { down = true }
        val result = newView(transport).acquireCandidateInLobby(null).get()

        assertEquals(ScheduleState.UNAVAILABLE, result.state(), "看不到大厅池时不能报「池里没有机器」")
        assertEquals("unavailable", result.failReason())
        assertNull(result.chosen())
    }

    @Test
    fun `控制面不可达且作用域把池成员全滤掉时归为空结果`() {
        populateLobby()
        val transport = LobbyTransport().apply { down = true }
        val nobody = AdmissionScope.of("example.zone.nobody", "true")
        val result = newView(transport).acquireCandidateInLobby(null, nobody).get()

        assertEquals(ScheduleState.NO_CANDIDATE, result.state(), "池里确实没有满足作用域的成员是稳定事实")
        assertEquals("no_candidate_in_scope", result.failReason())
        assertEquals(3, result.admissionExcludedCount(), "降级路径同样照实回报被作用域排除的台数")
    }

    // ---- ⑦ 接口缺省实现：能力缺失是最响的那一档 ----

    /** 最小实现：只满足 6 个抽象方法，4 条大厅方法完全靠接口 default。 */
    private class MinimalScheduling : BeaconScheduling {
        override fun acquireCandidate(zone: String): CompletableFuture<ScheduleResult> = CompletableFuture()

        override fun acquireCandidate(
            zone: String,
            purpose: String?,
        ): CompletableFuture<ScheduleResult> = CompletableFuture()

        override fun candidatesInZone(zone: String): List<CandidateView> = emptyList()

        override fun healthOf(serverId: String): HealthView? = null

        override fun selfHealth(): HealthView? = null

        override fun dataSource(): DataSourceState = DataSourceState(DataSource.LOCAL_SNAPSHOT, false, Long.MAX_VALUE)
    }

    @Test
    fun `接口缺省实现把大厅能力报为不支持（而不是忽略或空表）`() {
        val minimal = MinimalScheduling()

        assertFailsWith<UnsupportedOperationException> { minimal.acquireCandidateInLobby("lobby-transfer") }
        assertFailsWith<UnsupportedOperationException> { minimal.acquireCandidateInLobby("lobby-transfer", wantLobby) }
        assertFailsWith<UnsupportedOperationException> { minimal.candidatesInLobby() }
        assertFailsWith<UnsupportedOperationException> { minimal.candidatesInLobby(wantLobby) }
    }
}
