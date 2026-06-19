package top.wcpe.beacon.agent.adapters.messaging

import org.junit.jupiter.api.Assumptions
import redis.clients.jedis.JedisPool
import top.wcpe.beacon.agent.adapters.KotlinxJsonCodec
import top.wcpe.beacon.agent.core.messaging.MessageBus
import top.wcpe.beacon.agent.core.messaging.MessagingSettings
import java.util.UUID
import java.util.concurrent.CompletableFuture
import java.util.concurrent.CountDownLatch
import java.util.concurrent.ExecutionException
import java.util.concurrent.TimeUnit
import java.util.concurrent.TimeoutException
import kotlin.test.AfterTest
import kotlin.test.BeforeTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue
import kotlin.test.fail

/**
 * [RedisMessageTransport] 对真实 Redis（默认 localhost:16379，无密码）的集成测试。
 *
 * 用两个 [RedisMessageTransport]（serverA / serverB，连同一个 Redis）模拟两台子服，
 * 走完整的 [MessageBus] + [KotlinxJsonCodec] 链路，验证 ADR-0016 四种模式与离线补消费、
 * 以及 pub/sub「订阅激活窗口漏订阅竞态修复」的回归（onSubscribe 对账）。
 *
 * 环境约定：
 * - 测试库连接默认 host=localhost port=16379 无密码，可经 env 覆盖：
 *   BEACON_REDIS_TEST_HOST / BEACON_REDIS_TEST_PORT / BEACON_REDIS_TEST_PASSWORD。
 * - 连不上时用 [Assumptions.assumeTrue] 跳过（不让无 Redis 环境的 CI 红）。
 * - 隔离：每个用例用带随机后缀的唯一 serverId / topic，互不干扰、可重复跑。
 * - 清理：[AfterTest] 关闭传输并删除本次用到的 stream / 主题键 / 玩家名册项，跑完不留垃圾。
 */
class RedisMessageTransportIntegrationTest {

    private val host = System.getenv("BEACON_REDIS_TEST_HOST")?.takeIf { it.isNotBlank() } ?: "localhost"
    private val port = System.getenv("BEACON_REDIS_TEST_PORT")?.toIntOrNull() ?: 16379
    private val password = System.getenv("BEACON_REDIS_TEST_PASSWORD") ?: ""

    /** 本次用例的唯一前缀（隔离 + 便于清理）。 */
    private val runTag = "it-" + UUID.randomUUID().toString().take(8)

    private val settings = MessagingSettings(
        enabled = true,
        rpcTimeoutMs = 5000,
        streamMaxLen = 10000,
        consumerName = "it-consumer",
    )

    /** 本用例创建的传输实例，[AfterTest] 统一关闭。 */
    private val transports = mutableListOf<RedisMessageTransport>()

    /** 本用例使用到的 serverId（用于清理收件流 / 回信通道 / 消费组）。 */
    private val usedServerIds = mutableSetOf<String>()

    /** 本用例使用到的玩家名（用于清理名册 field）。 */
    private val usedPlayers = mutableSetOf<String>()

    private fun connection(): RedisConnection = RedisConnection(
        host = host,
        port = port,
        database = 0,
        password = password,
        connectTimeoutMs = 3000,
    )

    @BeforeTest
    fun guardRedisReachable() {
        // 守卫：探测测试 Redis 是否可连，连不上则整类跳过（不红）。
        val reachable = try {
            JedisPoolFactory.create(connection()).use { pool ->
                pool.resource.use { it.ping() == "PONG" }
            }
        } catch (t: Throwable) {
            false
        }
        Assumptions.assumeTrue(reachable, "测试 Redis 不可达（$host:$port），跳过集成测试")
    }

    @AfterTest
    fun cleanup() {
        // 先关闭所有传输（停消费/订阅线程、释放连接池）。
        for (t in transports) {
            try {
                t.close()
            } catch (_: Throwable) {
                // 清理阶段忽略关闭异常。
            }
        }
        transports.clear()
        // 再删本次用到的 Redis 键，保证可重复跑、不留垃圾。
        try {
            JedisPoolFactory.create(connection()).use { pool ->
                pool.resource.use { jedis ->
                    for (sid in usedServerIds) {
                        jedis.del(RedisChannels.serverInbox(sid))
                    }
                    // pub/sub 回信/主题信道无持久键，无需删；玩家名册需删本次写入的 field。
                    for (player in usedPlayers) {
                        jedis.hdel(RedisChannels.PLAYER_LOCATION_HASH, player)
                    }
                }
            }
        } catch (_: Throwable) {
            // 清理阶段忽略异常。
        }
    }

    /** 构造一个传输实例并登记，便于统一关闭与清理。 */
    private fun newTransport(serverId: String): RedisMessageTransport {
        usedServerIds.add(serverId)
        val transport = RedisMessageTransport(
            connection = connection(),
            serverId = serverId,
            settings = settings,
        )
        transports.add(transport)
        return transport
    }

    /** 用真实传输 + 真实 codec 构造 MessageBus（注入传输的 playerLocator）。 */
    private fun newBus(serverId: String, transport: RedisMessageTransport): MessageBus = MessageBus(
        transport = transport,
        codec = KotlinxJsonCodec(),
        selfServerId = serverId,
        settings = settings,
        playerLocator = transport.playerLocator(),
    )

    @Test
    fun `定向发送 含离线补消费 B 后启动经消费组补收`() {
        val idA = "$runTag-A"
        val idB = "$runTag-B"
        val transA = newTransport(idA)
        val transB = newTransport(idB)
        val busA = newBus(idA, transA)
        val busB = newBus(idB, transB)

        busA.start()
        // 关键：先确保 B 的收件流消费组已建好（这样 A 早发的消息会留存），
        // 但 B 的消费回调尚未"消费"——通过先 ensureConsumerGroup、后注册 handler 再 start 来模拟离线补消费。
        // 这里直接用：A 先发 → B 后 start，验证消费组从 0 起补消费历史消息。
        // 由于 mkStream=true 在 B.start() 时才建组，B 启动后从 0-0 读取，能补到 A 之前写入的消息。

        val latch = CountDownLatch(1)
        var received: Any? = null
        busB.on("evt") { ctx ->
            received = ctx.payload()
            latch.countDown()
        }

        // B 尚未 start（离线）：A 先发可靠消息，写入 B 的收件流留存。
        busA.send(idB, "evt", mapOf("n" to 42L))

        // B 上线：start 建消费组（从 0-0），补消费到离线期间留存的消息。
        busB.start()

        assertTrue(latch.await(10, TimeUnit.SECONDS), "B 上线后未在超时内补收到离线消息")
        assertEquals(mapOf("n" to 42L), received)
    }

    @Test
    fun `RPC A call B 超时内拿到回信 B 不处理则超时`() {
        val idA = "$runTag-rpcA"
        val idB = "$runTag-rpcB"
        val idC = "$runTag-rpcC"
        val transA = newTransport(idA)
        val transB = newTransport(idB)
        val busA = newBus(idA, transA)
        val busB = newBus(idB, transB)

        busA.start()
        busB.start()

        // B 注册处理器并回信。
        busB.on("sum") { ctx ->
            val args = ctx.payload() as Map<*, *>
            val x = (args["x"] as Number).toLong()
            val y = (args["y"] as Number).toLong()
            ctx.reply(mapOf("result" to x + y))
        }
        // 等待 B 的回信/收件订阅线程激活（pub/sub 需要时间建立）。
        waitConnected(busB)
        waitConnected(busA)

        val future: CompletableFuture<Any?> = busA.call(idB, "sum", mapOf("x" to 2L, "y" to 3L))
        val resp = future.get(8, TimeUnit.SECONDS) as Map<*, *>
        assertEquals(5L, (resp["result"] as Number).toLong())

        // 目标 C 在线但无 "noop" 处理器：A call C 应在 rpcTimeoutMs 内超时（无回信）。
        val transC = newTransport(idC)
        val busC = newBus(idC, transC)
        busC.start()
        waitConnected(busC)

        val timeoutFuture = busA.call(idC, "noop", null)
        val ex = try {
            timeoutFuture.get(10, TimeUnit.SECONDS)
            fail("期望 RPC 超时，但拿到了返回值")
        } catch (e: ExecutionException) {
            e
        }
        assertTrue(ex.cause is TimeoutException, "期望 TimeoutException，实际=${ex.cause}")
    }

    @Test
    fun `主题 pub sub B 订阅 A 发布 B 收到 未订阅不收`() {
        val idA = "$runTag-topicA"
        val idB = "$runTag-topicB"
        val topicSubscribed = "$runTag-news"
        val topicOther = "$runTag-sports"
        val transA = newTransport(idA)
        val transB = newTransport(idB)
        val busA = newBus(idA, transA)
        val busB = newBus(idB, transB)

        busA.start()
        busB.start()

        val latch = CountDownLatch(1)
        var received: Any? = null
        busB.subscribe(topicSubscribed) { msg ->
            received = msg.payload
            latch.countDown()
        }
        // 等待订阅在 Redis 侧真正激活（pub/sub 无留存，发早了会丢）。
        waitTopicSubscribed(topicSubscribed)

        // 未订阅主题：发了应丢弃（不影响下面断言）。
        busA.publish(topicOther, mapOf("x" to 1L))
        // 已订阅主题：应收到。
        busA.publish(topicSubscribed, mapOf("headline" to "hello"))

        assertTrue(latch.await(10, TimeUnit.SECONDS), "订阅者未在超时内收到已订阅主题消息")
        assertEquals(mapOf("headline" to "hello"), received)
    }

    @Test
    fun `按玩家寻址 名册命中定向投递 落空兜底不投`() {
        val idA = "$runTag-locA"
        val idB = "$runTag-locB"
        val player = "$runTag-Steve"
        val ghost = "$runTag-Ghost"
        usedPlayers.add(player)
        usedPlayers.add(ghost)
        val transA = newTransport(idA)
        val transB = newTransport(idB)
        val busA = newBus(idA, transA)
        val busB = newBus(idB, transB)

        busA.start()
        busB.start()

        // 用 RedisPlayerRoster 写名册：player 在 B。
        val rosterPool: JedisPool = JedisPoolFactory.create(connection())
        try {
            val roster = RedisPlayerRoster(rosterPool)
            roster.onPlayerLocated(player, idB)

            val latch = CountDownLatch(1)
            var received: Any? = null
            busB.on("dm") { ctx ->
                received = ctx.payload()
                latch.countDown()
            }

            // 名册命中：解析到 B 并定向投递（经 B 收件流 → 补消费）。
            val delivered = busA.sendToPlayer(player, "dm", "hello")
            assertTrue(delivered, "名册命中应返回 true")
            assertTrue(latch.await(10, TimeUnit.SECONDS), "按玩家寻址未在超时内送达目标")
            assertEquals("hello", received)

            // 名册落空：ghost 不在名册，应返回 false 不投递。
            val notDelivered = busA.sendToPlayer(ghost, "dm", "x")
            assertFalse(notDelivered, "名册落空应返回 false")
        } finally {
            rosterPool.close()
        }
    }

    @Test
    fun `名册全表读 HGETALL 返回 proxy 写入的全部条目 删后不再含`() {
        // FR-31 / ADR-0022：rosterDirectory().snapshot() 走 HGETALL beacon:player-loc，全表读名册。
        val idA = "$runTag-rosterA"
        val idB = "$runTag-rosterB"
        val p1 = "$runTag-Steve"
        val p2 = "$runTag-Alex"
        usedPlayers.add(p1)
        usedPlayers.add(p2)
        val transA = newTransport(idA)
        transA.start()
        waitConnected(newBus(idA, transA))

        val rosterPool: JedisPool = JedisPoolFactory.create(connection())
        try {
            val roster = RedisPlayerRoster(rosterPool)
            // proxy 写名册：p1 在 A、p2 在 B。
            roster.onPlayerLocated(p1, idA)
            roster.onPlayerLocated(p2, idB)

            val directory = transA.rosterDirectory()
            val snapshot = directory.snapshot()
            // 全表读应至少含本次写入的两条（同库可能有他例残留，故只断言包含关系）。
            assertEquals(idA, snapshot[p1], "名册全表读应含 p1→A")
            assertEquals(idB, snapshot[p2], "名册全表读应含 p2→B")

            // 删除 p1 后再读：不应再含 p1，p2 仍在。
            roster.onPlayerQuit(p1, idA)
            val after = transA.rosterDirectory().snapshot()
            assertFalse(after.containsKey(p1), "删除后全表读不应再含 p1")
            assertEquals(idB, after[p2], "p2 仍应在名册中")
        } finally {
            rosterPool.close()
        }
    }

    @Test
    fun `pub sub 激活窗口并发订阅多主题 逐一发布全收到不漏`() {
        // 回归：onSubscribe 对账修复——在传输层「快照后、激活前」窗口并发追加多个主题，
        // 验证激活后对账补订阅、逐一 publish 全部收到不漏（漏订阅竞态曾导致部分主题收不到）。
        //
        // 复现要点：B 必须先 start 并连上（否则 MessageBus.subscribe 的 requireAvailable 直接抛），
        // 此时 pub/sub 线程刚激活、订阅尚未真正生效，正是竞态窗口。随后多线程并发 subscribe，
        // 让各主题落进「activePubSub 已设、isSubscribed 尚 false」的窗口，考验 onSubscribe 对账补订阅。
        // 为最大化命中概率，多次循环、每轮在 B 全新 start 后立刻并发订阅一批主题。
        val idA = "$runTag-raceA"
        val busA = newBus(idA, newTransport(idA))
        busA.start()
        waitConnected(busA)

        val rounds = 5
        val topicsPerRound = 12
        for (round in 0 until rounds) {
            val idB = "$runTag-raceB-$round"
            val transB = newTransport(idB)
            val busB = newBus(idB, transB)
            // B 先连上：进入「pub/sub 线程刚起、订阅尚未激活」的窗口。
            busB.start()
            waitConnected(busB)

            val topics = (0 until topicsPerRound).map { "$runTag-race-$round-topic-$it" }
            val latches = topics.associateWith { CountDownLatch(1) }

            // 并发订阅：所有线程在同一放闸点齐发 subscribe，集中砸进激活窗口，制造并发加入竞态。
            val startGate = CountDownLatch(1)
            val subscribeThreads = topics.map { topic ->
                Thread {
                    startGate.await()
                    busB.subscribe(topic) { _ -> latches.getValue(topic).countDown() }
                }.also { it.isDaemon = true }
            }
            subscribeThreads.forEach { it.start() }
            startGate.countDown()
            subscribeThreads.forEach { it.join(10_000) }

            // 等所有主题都在 Redis 侧激活订阅（对账补订阅完成）。
            for (topic in topics) {
                assertTrue(
                    waitTopicSubscribed(topic, timeoutMs = 10_000),
                    "主题未在超时内完成订阅激活（疑似漏订阅竞态）：round=$round topic=$topic",
                )
            }

            // 逐一发布，验证每个主题都收到（不漏）。
            for (topic in topics) {
                busA.publish(topic, mapOf("t" to topic))
            }

            val missed = topics.filter { !latches.getValue(it).await(10, TimeUnit.SECONDS) }
            assertTrue(missed.isEmpty(), "以下主题未收到消息（漏订阅竞态回归失败）：round=$round $missed")

            busB.close()
        }
    }

    // ---- 测试辅助 ----

    /** 轮询等待 bus 的 transport 连上（isConnected）。 */
    private fun waitConnected(bus: MessageBus, timeoutMs: Long = 5000) {
        val deadline = System.currentTimeMillis() + timeoutMs
        while (System.currentTimeMillis() < deadline) {
            if (bus.isAvailable()) return
            Thread.sleep(50)
        }
    }

    /**
     * 轮询等待某主题在 Redis 侧真正有订阅者（用 PUBSUB NUMSUB 查询订阅计数）。
     *
     * pub/sub 不留存：必须确认 Redis 已记录订阅者后再 publish，否则消息丢失属环境时序、非 bug。
     */
    private fun waitTopicSubscribed(
        topic: String,
        timeoutMs: Long = 5000,
    ): Boolean {
        val channel = RedisChannels.topic(topic)
        val deadline = System.currentTimeMillis() + timeoutMs
        JedisPoolFactory.create(connection()).use { pool ->
            while (System.currentTimeMillis() < deadline) {
                val count = try {
                    pool.resource.use { jedis ->
                        // PUBSUB NUMSUB 返回 [channel, count, ...]，取该信道的订阅计数。
                        val result = jedis.pubsubNumSub(channel)
                        result[channel]?.toLong() ?: 0L
                    }
                } catch (t: Throwable) {
                    0L
                }
                if (count > 0) return true
                Thread.sleep(50)
            }
        }
        return false
    }
}
