package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.api.DeclarationApplied
import top.wcpe.beacon.agent.api.DeclarationOutcome
import top.wcpe.beacon.agent.api.NodeDeclaration
import top.wcpe.beacon.agent.api.SelfDeclaration
import top.wcpe.beacon.agent.core.api.NodeDeclarationHolder
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.config.ConfigApplier
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.BackoffSettings
import top.wcpe.beacon.agent.core.settings.FileTreeSettings
import top.wcpe.beacon.agent.core.settings.OverrideSettings
import top.wcpe.beacon.agent.core.testutil.CannedJsonCodec
import top.wcpe.beacon.agent.core.testutil.FakeBeaconBackend
import top.wcpe.beacon.agent.core.testutil.ThreadPoolPlatformAdapter
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.TimeUnit
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals

/**
 * 节点自声明门面与生命周期的接线单测（FR-243，见规格 §3.3「启动竞态」）：
 * 装配期降级 → 注册成功后激活真实实现并自动补报一次 → 停机复位，
 * 全程经真实 [AgentLifecycle] 的既有钩子（`onRegistered` / `shutdownListeners`）驱动。
 *
 * 控制面 HTTP 与结果映射由 BeaconApiClientDeclarationTest / SelfDeclarationViewTest 覆盖，
 * 本用例只钉住「何时激活、补报几次、何时复位」这条时机链。
 */
class AgentLifecycleDeclarationHookTest {
    private val backend = FakeBeaconBackend()
    private val adapter = ThreadPoolPlatformAdapter()
    private val store = EffectiveConfigStore()

    /** 真实实现的替身：记录收到的声明并回 APPLIED（供「补报恰好一次」计数断言）。 */
    private class RecordingDeclaration : SelfDeclaration {
        val declarations = CopyOnWriteArrayList<NodeDeclaration>()

        override fun declare(declaration: NodeDeclaration?): DeclarationOutcome {
            val declared = declaration!!
            declarations.add(declared)
            return DeclarationOutcome.applied(
                DeclarationApplied(declared.capacity().orElse(null), declared.labels()),
            )
        }
    }

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
        )

    private fun settings() =
        AgentSettings(
            endpoints = listOf("http://localhost:8848"),
            bootstrapToken = "tk",
            pollTimeoutMs = 50,
            requestTimeoutMs = 200,
            heartbeatFallbackMs = 100_000,
            // 退避初始值给大，避免延迟重试在测试窗口内自调度干扰断言。
            backoff = BackoffSettings(initialMs = 60_000, maxMs = 60_000, multiplier = 1.0, jitterRatio = 0.0),
            snapshotEnabled = false,
            snapshotFileName = "snapshot.json",
            fileTree = FileTreeSettings(enabled = false, targetSubDir = "", appliedManifestFileName = "file-tree.applied.json"),
            override = OverrideSettings(commandWhitelist = emptySet(), backupDirName = "override-backup"),
        )

    private fun newLifecycle(): AgentLifecycle {
        val apiClient = BeaconApiClient(backend, CannedJsonCodec(), settings())
        val applier = ConfigApplier(store, null, adapter)
        return AgentLifecycle(identity(), settings(), adapter, apiClient, store, applier, null)
    }

    @AfterTest
    fun tearDown() {
        adapter.shutdown()
    }

    @Test
    fun `装配期降级注册成功后自动补报一次停机后复位`() {
        val holder = NodeDeclarationHolder()
        val delegate = RecordingDeclaration()
        val lifecycle = newLifecycle()
        // 与 AgentAssembly 的接线同款：注册成功激活真实实现，停机复位。
        lifecycle.onRegistered { holder.set(delegate) }
        lifecycle.shutdownListeners.add { holder.reset() }

        // 装配期（已装配但尚未注册成功）：如实回 UNAVAILABLE，并记住本次声明。
        assertEquals(
            DeclarationOutcome.Status.UNAVAILABLE,
            holder.declare(NodeDeclaration.ofCapacity(200)).status(),
            "注册成功前应如实回通道不可用",
        )

        lifecycle.bootstrapWithSnapshotThenConnect()
        waitUntil(3000) { delegate.declarations.isNotEmpty() }

        assertEquals(1, delegate.declarations.size, "注册成功后应自动补报恰好一次")
        assertEquals(200, delegate.declarations[0].capacity().orElse(null), "补报的应是未就绪期那次声明")

        // 就绪后：声明直接透传到真实实现。
        assertEquals(
            DeclarationOutcome.Status.APPLIED,
            holder.declare(NodeDeclaration.ofCapacity(150)).status(),
            "注册成功后声明应直达真实实现",
        )
        assertEquals(2, delegate.declarations.size)

        // 停机 → 复位为降级态，不再触达真实实现。
        lifecycle.shutdown()
        assertEquals(
            DeclarationOutcome.Status.UNAVAILABLE,
            holder.declare(NodeDeclaration.ofCapacity(50)).status(),
            "停机后应复位为降级态",
        )
        assertEquals(2, delegate.declarations.size, "停机后不得再触达真实实现")
    }

    private fun waitUntil(
        timeoutMs: Long,
        cond: () -> Boolean,
    ) {
        val deadline = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(timeoutMs)
        while (System.nanoTime() < deadline) {
            if (cond()) return
            Thread.sleep(10)
        }
    }
}
