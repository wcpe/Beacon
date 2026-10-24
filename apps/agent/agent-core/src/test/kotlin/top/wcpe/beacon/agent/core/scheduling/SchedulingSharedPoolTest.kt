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
import top.wcpe.beacon.agent.core.client.SharedPoolCandidates
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
 * 共享游戏池调度的单测（FR-281）。
 *
 * 锁四件事：
 * ① 两个池**互相隔离**：共享池成员不出现在大厅段，大厅成员不出现在共享池段——
 *    这是「玩家从 BC 进来不会被丢进游戏服」的机制级保证；
 * ② 读不出共享池时抛，模型在而池空才返回空表（与大厅同口径）；
 * ③ 下发口径：共享池档发 `scope=shared_pool` 且**不带** zone；
 * ④ 三态可分：控制面 4xx 是"没问成"（不可用），池空才是"没有候选"；降级不入补报队列。
 */
class SchedulingSharedPoolTest {
    private val tempDir: File = Files.createTempDirectory("sched-shared").toFile()
    private val adapter = ManualSchedAdapter(tempDir)
    private val cache = SchedulingCache(now = { 10_000L })
    private val reportQueue = LocalDecisionReportQueue()
    private val selfHealthHolder = SelfHealthHolder(now = { 10_000L })

    private val wantGame = AdmissionScope.of("example.zone.game-a", "true")

    /** 只路由 decide / candidates 两端点；decide 带请求体，故记下最后一次编码的树。 */
    private class SharedTransport : HttpTransport {
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

    private class SharedCodec(
        private val decideBody: Map<String, Any?>,
    ) : JsonCodec {
        val lastEncoded = AtomicReference<Any?>(null)

        override fun encode(value: Any?): String {
            lastEncoded.set(value)
            return "encoded"
        }

        override fun decode(json: String): Any? =
            when (json) {
                SharedTransport.DECIDE -> decideBody
                else -> emptyMap<String, Any?>()
            }
    }

    private fun newView(
        transport: SharedTransport = SharedTransport(),
        codec: SharedCodec = SharedCodec(chosenBody("game-1", 92)),
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
        "traceId" to "srv-shared-trace",
        "chosen" to mapOf("serverId" to serverId, "score" to score),
        "candidateCount" to 2,
        "excludedCount" to 0,
    )

    @Suppress("UNCHECKED_CAST")
    private fun encodedBody(codec: SharedCodec): Map<String, Any?> = codec.lastEncoded.get() as Map<String, Any?>

    /** 一帧同时带大厅段与共享池段（两池成员不重叠，与控制面归属四选一一致）。 */
    private fun snapshotWithBothPools(
        lobbyCandidates: List<CandidateEntry>,
        sharedCandidates: List<CandidateEntry>,
    ): CandidateSnapshot =
        CandidateSnapshot(
            1_000L,
            10_000L,
            emptyMap(),
            LobbyCandidates(7L, lobbyCandidates.isNotEmpty(), lobbyCandidates),
            SharedPoolCandidates(9L, sharedCandidates.isNotEmpty(), sharedCandidates),
        )

    /** 三台共享池成员：一台不限制服务范围、一台服务本游戏池、一台只服务别处。 */
    private fun populateSharedPool() {
        cache.set(
            snapshotWithBothPools(
                lobbyCandidates = listOf(candidateEntry("lobby-1", 60, labelsPresent = true)),
                sharedCandidates =
                    listOf(
                        candidateEntry("game-free", 90, online = 5, labels = mapOf("example.zones" to ""), labelsPresent = true),
                        candidateEntry("game-a", 70, online = 8, labels = mapOf("example.zone.game-a" to "true"), labelsPresent = true),
                        candidateEntry("game-other", 99, labels = mapOf("example.zone.other" to "true"), labelsPresent = true),
                    ),
            ),
            live = true,
        )
    }

    // ---- ① 两池互相隔离 ----

    @Test
    fun `共享池成员不出现在大厅段且大厅成员不出现在共享池段`() {
        populateSharedPool()
        val view = newView()

        val lobbyIds = view.candidatesInLobby().map { it.serverId() }
        val sharedIds = view.candidatesInSharedPool().map { it.serverId() }

        assertEquals(listOf("lobby-1"), lobbyIds, "大厅段只应含入口大厅成员")
        assertEquals(listOf("game-free", "game-a", "game-other"), sharedIds, "共享池段只应含共享池成员")
        assertTrue(
            lobbyIds.intersect(sharedIds.toSet()).isEmpty(),
            "两池成员集合必须不重叠：重叠会让 BC 落脚把玩家送进游戏服，或反之",
        )
    }

    // ---- ② 读不出共享池时抛，池空才是空表 ----

    @Test
    fun `本帧没有共享池段时读取抛而不是返回空表`() {
        // 旧控制面：候选帧里没有 shared 段（这里有 lobby 段，证明抛出只因共享池模型缺失）。
        cache.set(
            CandidateSnapshot(
                1_000L,
                10_000L,
                emptyMap(),
                LobbyCandidates(7L, true, listOf(candidateEntry("lobby-1", 60, labelsPresent = true))),
            ),
            live = true,
        )
        val view = newView()

        val failure = assertFailsWith<IllegalStateException> { view.candidatesInSharedPool() }
        assertTrue(
            failure.message!!.contains("共享"),
            "报因要指名是「没有共享池模型」，否则会被读成「池里没有机器」",
        )
        // 大厅档不受影响（两段各自独立）。
        assertEquals(listOf("lobby-1"), view.candidatesInLobby().map { it.serverId() })
    }

    @Test
    fun `共享池模型在但池里没有成员时返回空表`() {
        cache.set(snapshotWithBothPools(lobbyCandidates = emptyList(), sharedCandidates = emptyList()), live = true)

        assertEquals(emptyList(), newView().candidatesInSharedPool().map { it.serverId() })
    }

    @Test
    fun `共享池候选按作用域收窄且视图 zone 为空串`() {
        populateSharedPool()
        val view = newView()

        assertEquals(
            listOf("game-a"),
            view.candidatesInSharedPool(wantGame).map { it.serverId() },
            "非空作用域只留满足者",
        )
        assertTrue(
            view.candidatesInSharedPool(wantGame).all { it.zone() == "" },
            "共享池成员不属于任何小区，视图 zone 必须是空串",
        )
    }

    // ---- ③ 下发口径 ----

    @Test
    fun `共享池决策下发 scope等于shared_pool 且不带 zone 键`() {
        populateSharedPool()
        val codec = SharedCodec(chosenBody("game-a", 70))
        newView(codec = codec).acquireCandidateInSharedPool("room-open").get()

        val body = encodedBody(codec)
        assertEquals("shared_pool", body["scope"], "不下发 scope 时控制面会按缺省的 zone 档处理，结论口径完全不同")
        assertTrue(
            !body.containsKey("zone"),
            "共享池档不能带 zone：控制面要求该档 zone 必须为空",
        )
    }

    @Test
    fun `小区档与大厅档请求体都不含 shared_pool 的 scope 值`() {
        populateSharedPool()
        val codec = SharedCodec(chosenBody("game-a", 70))
        newView(codec = codec).acquireCandidate("z-a", "zone-transfer", null).get()

        assertTrue(
            !encodedBody(codec).containsKey("scope"),
            "小区档不得下发 scope 键：老控制面按缺省 zone 档处理",
        )
    }

    // ---- ④ 三态与降级 ----

    @Test
    fun `共享池控制面回 400 时归为不可用而不是没有候选`() {
        populateSharedPool()
        val transport = SharedTransport().apply { decideStatus = 400 }
        val codec = SharedCodec(mapOf("code" to "INVALID_PARAM"))
        val result = newView(transport, codec).acquireCandidateInSharedPool("room-open").get()

        assertEquals(ScheduleState.UNAVAILABLE, result.state(), "4xx 是「没问成」，不是「确实没有候选」")
        assertEquals("INVALID_PARAM", result.failReason())
        assertNull(result.chosen())
    }

    @Test
    fun `共享池控制面回无候选时归为空结果`() {
        cache.set(snapshotWithBothPools(lobbyCandidates = emptyList(), sharedCandidates = emptyList()), live = true)
        val codec =
            SharedCodec(
                mapOf("traceId" to "t-shared", "chosen" to null, "candidateCount" to 0, "failReason" to "no_candidate"),
            )
        val result = newView(codec = codec).acquireCandidateInSharedPool(null).get()

        assertEquals(ScheduleState.NO_CANDIDATE, result.state(), "池空是稳定事实")
        assertEquals("no_candidate", result.failReason())
    }

    @Test
    fun `控制面不可达时用共享池段本地降级仍能出结果且不入补报队列`() {
        populateSharedPool()
        val transport = SharedTransport().apply { down = true }
        val future = newView(transport).acquireCandidateInSharedPool("room-open")
        val result = future.get()

        assertTrue(!future.isCompletedExceptionally, "fail-static：future 绝不异常完成")
        assertEquals(DecisionSource.LOCAL_FALLBACK, result.source())
        assertEquals(ScheduleState.CHOSEN, result.state())
        assertEquals("game-other", result.chosen()?.serverId(), "本地 highest_score 应选分最高者（99）")
        assertEquals("", result.chosen()?.zone())
        assertEquals(
            0,
            reportQueue.size(),
            "共享池降级决策不得入补报队列（空 zone 会让整批 400 并堵住后续补报）",
        )
    }

    @Test
    fun `选中时视图 zone 为空串并从共享池段补全展示字段`() {
        populateSharedPool()
        val codec = SharedCodec(chosenBody("game-a", 70))
        val result = newView(codec = codec).acquireCandidateInSharedPool("room-open").get()

        assertEquals(ScheduleState.CHOSEN, result.state())
        val chosen = result.chosen()!!
        assertEquals("game-a", chosen.serverId())
        assertEquals("", chosen.zone(), "共享池成员不属于任何小区")
        assertEquals(8, chosen.onlineCount(), "在线人数应取自共享池段")
        assertEquals(100, chosen.maxOnline())
        assertEquals("srv-shared-trace", result.traceId())
    }

    @Test
    fun `healthOf 覆盖共享池成员`() {
        populateSharedPool()

        val health = newView().healthOf("game-a")
        assertEquals(70, health?.score(), "池成员没有 zone，只查小区段会让它的 healthOf 恒为 null")
        assertEquals(HealthLevel.HEALTHY, health?.level())
    }

    // ---- ⑤ 缺省实现与兜底实现 ----

    /** 最小实现：只满足 6 个抽象方法，共享池 4 条完全靠接口 default。 */
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
    fun `接口缺省实现把共享池能力报为不支持`() {
        val minimal = MinimalScheduling()

        assertFailsWith<UnsupportedOperationException> { minimal.acquireCandidateInSharedPool("room-open") }
        assertFailsWith<UnsupportedOperationException> { minimal.acquireCandidateInSharedPool("room-open", wantGame) }
        assertFailsWith<UnsupportedOperationException> { minimal.candidatesInSharedPool() }
        assertFailsWith<UnsupportedOperationException> { minimal.candidatesInSharedPool(wantGame) }
    }
}
