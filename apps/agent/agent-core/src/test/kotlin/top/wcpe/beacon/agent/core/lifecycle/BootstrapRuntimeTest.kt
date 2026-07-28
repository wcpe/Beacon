package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.identity.IdentityBindingSnapshotStore
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.BackoffSettings
import top.wcpe.beacon.agent.core.settings.FileTreeSettings
import top.wcpe.beacon.agent.core.settings.OverrideSettings
import top.wcpe.beacon.agent.core.testutil.ThreadPoolPlatformAdapter
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.nio.file.Files
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

class BootstrapRuntimeTest {
    private val adapter = ThreadPoolPlatformAdapter()

    @AfterTest
    fun tearDown() = adapter.shutdown()

    @Test
    fun `pending 时不构造 active runtime`() {
        val activeCalls = AtomicInteger()
        val runtime = runtime(HttpResponse(202, "pending"), activeCalls, CountDownLatch(1))

        runtime.start()
        Thread.sleep(100)

        assertEquals(0, activeCalls.get())
    }

    @Test
    fun `控制面解绑时终止且不构造 active runtime`() {
        val activeCalls = AtomicInteger()
        val terminated = CountDownLatch(1)
        val runtime = runtime(HttpResponse(200, "unbound"), activeCalls, terminated)

        runtime.start()

        assertTrue(terminated.await(2, TimeUnit.SECONDS))
        assertEquals(0, activeCalls.get())
    }

    @Test
    fun `有效绑定快照在控制面不可用时受限启动且只启动一次`() {
        val activeCalls = AtomicInteger()
        val active = CountDownLatch(1)
        val runtime =
            runtime(
                response = HttpResponse(503, "unavailable"),
                activeCalls = activeCalls,
                terminated = CountDownLatch(1),
                snapshot = validSnapshot(),
                onActive = { active.countDown() },
            )

        runtime.start()

        assertTrue(active.await(2, TimeUnit.SECONDS))
        Thread.sleep(50)
        assertEquals(1, activeCalls.get())
    }

    @Test
    fun `空或无效快照在控制面不可用时不得启动 active runtime`() {
        val activeCalls = AtomicInteger()
        val runtime =
            runtime(
                response = HttpResponse(503, "unavailable"),
                activeCalls = activeCalls,
                terminated = CountDownLatch(1),
                snapshot = invalidSnapshot(),
            )

        runtime.start()
        Thread.sleep(100)

        assertEquals(0, activeCalls.get())
    }

    @Test
    fun `离线快照启动后控制面解除绑定时立即终止并失效快照`() {
        val activeCalls = AtomicInteger()
        val terminated = CountDownLatch(1)
        val calls = AtomicInteger()
        val file = Files.createTempFile("binding", ".json").toFile()
        val runtime =
            runtime(
                responseProvider = {
                    if (calls.getAndIncrement() == 0) HttpResponse(503, "unavailable") else HttpResponse(200, "unbound")
                },
                activeCalls = activeCalls,
                terminated = terminated,
                snapshot = validSnapshot(),
                snapshotFile = file,
            )

        runtime.start()

        assertTrue(terminated.await(2, TimeUnit.SECONDS))
        assertEquals(1, activeCalls.get())
        assertTrue(!file.exists())
    }

    @Test
    fun `离线快照启动后控制面绑定不一致时立即终止并失效快照`() {
        val activeCalls = AtomicInteger()
        val terminated = CountDownLatch(1)
        val calls = AtomicInteger()
        val file = Files.createTempFile("binding", ".json").toFile()
        val runtime =
            runtime(
                responseProvider = {
                    if (calls.getAndIncrement() == 0) HttpResponse(503, "unavailable") else HttpResponse(200, "active-mismatch")
                },
                activeCalls = activeCalls,
                terminated = terminated,
                snapshot = validSnapshot(),
                snapshotFile = file,
            )

        runtime.start()

        assertTrue(terminated.await(2, TimeUnit.SECONDS))
        assertEquals(1, activeCalls.get())
        assertTrue(!file.exists())
    }

    @Test
    fun `控制面兼容地址变化不使同一绑定快照失效`() {
        val activeCalls = AtomicInteger()
        val terminated = CountDownLatch(1)
        val runtime = runtime(HttpResponse(200, "active-address-changed"), activeCalls, terminated, snapshot = validSnapshot())

        runtime.start()

        Thread.sleep(100)
        assertEquals(1, activeCalls.get())
        assertEquals(1L, terminated.count)
    }

    private fun runtime(
        response: HttpResponse,
        activeCalls: AtomicInteger,
        terminated: CountDownLatch,
        snapshot: Map<String, Any?> = emptyMap(),
        onActive: () -> Unit = {},
    ): BootstrapRuntime =
        runtime(
            responseProvider = { response },
            activeCalls = activeCalls,
            terminated = terminated,
            snapshot = snapshot,
            onActive = onActive,
        )

    private fun runtime(
        responseProvider: () -> HttpResponse,
        activeCalls: AtomicInteger,
        terminated: CountDownLatch,
        snapshot: Map<String, Any?>,
        snapshotFile: java.io.File = Files.createTempFile("binding", ".json").toFile(),
        onActive: () -> Unit = {},
    ): BootstrapRuntime {
        val codec =
            object : JsonCodec {
                override fun encode(value: Any?): String = "{}"

                override fun decode(json: String): Any? =
                    when (json) {
                        "snapshot" -> snapshot
                        "active-mismatch" ->
                            validSnapshot().toMutableMap().apply {
                                put("status", "active")
                                put("serverId", "other-server")
                            }
                        "active-address-changed" ->
                            validSnapshot().toMutableMap().apply {
                                put("status", "active")
                                put("address", "203.0.113.8:25565")
                            }
                        else -> mapOf("status" to json)
                    }
            }
        val transport = object : HttpTransport {
            override fun execute(request: HttpRequest): HttpResponse = responseProvider()
        }
        val identity =
            AgentIdentity(
                namespace = "",
                serverId = "",
                role = "bukkit",
                groupHint = "",
                address = "",
                version = "",
                capacity = 0,
                weight = 0,
                metadata = emptyMap(),
                identityId = "identity",
                bootId = "boot",
            )
        return BootstrapRuntime(
            identity = identity,
            settings = settings(),
            adapter = adapter,
            apiClient = BeaconApiClient(transport, codec, settings()),
            snapshots = IdentityBindingSnapshotStore(snapshotFile.apply { writeText("snapshot") }, codec),
            onActive = { _, _ -> activeCalls.incrementAndGet(); onActive() },
            onTerminal = { terminated.countDown() },
        )
    }

    private fun settings() =
        AgentSettings(
            endpoints = listOf("http://localhost:8848"),
            bootstrapToken = "tk",
            pollTimeoutMs = 50,
            requestTimeoutMs = 10,
            heartbeatFallbackMs = 10_000,
            backoff = BackoffSettings(1000, 1000, 1.0, 0.0),
            snapshotEnabled = false,
            snapshotFileName = "snapshot.json",
            fileTree = FileTreeSettings(false, "", "files.json"),
            override = OverrideSettings(emptySet(), "backup"),
        )

    private fun validSnapshot() =
        mapOf(
            "formatVersion" to 1,
            "identityId" to "identity",
            "kind" to "bukkit",
            "namespace" to "prod",
            "serverId" to "lobby-1",
            "boundAt" to "2026-07-28T12:00:00Z",
            "bindingFingerprint" to "a".repeat(64),
            "address" to "203.0.113.10:25565",
        )

    private fun invalidSnapshot() = mapOf("formatVersion" to 1)
}
