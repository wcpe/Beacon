package top.wcpe.beacon.agent.core.settings

import top.wcpe.beacon.agent.api.ConfigChangeListener
import top.wcpe.beacon.agent.core.api.EffectiveConfigView
import top.wcpe.beacon.agent.core.client.BeaconApiClient
import top.wcpe.beacon.agent.core.config.EffectiveConfigStore
import top.wcpe.beacon.agent.core.identity.AgentCredentialsStore
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.testsupport.ManualAsyncAdapter
import top.wcpe.beacon.agent.core.testsupport.TestJsonCodec
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import java.nio.file.Files
import java.nio.file.Path
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicReference
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertSame
import kotlin.test.assertTrue

/**
 * [PushedConfigCoordinator] 的下发应用用例。
 *
 * ## 本文件按分节维护
 *
 * 多个并行改动会各自往这里加用例，故**按分节追加、不要改动其它分节的用例**
 * （各自新建协调器、互不共享状态）。当前分节：
 *  - §A reload 并发位（`delivery.reload-pool-size`）的下发与收口
 *  - §E 重载超时的下发与收口、断连缓存、删项回退、拉取防重入（E 组）
 */
class PushedConfigCoordinatorTest {
    /** 返回固定 settings 报文的传输层：把「控制面下发了什么」直接摆到台面上。 */
    private class FixedSettingsTransport(
        values: Map<String, String>,
    ) : HttpTransport {
        private val body =
            TestJsonCodec.encode(
                mapOf(
                    "values" to values,
                    "namespace" to "prod",
                    "serverId" to "lobby-1",
                    "rejectedKeys" to emptyList<String>(),
                ),
            )

        override fun execute(request: HttpRequest): HttpResponse = HttpResponse(200, body)
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
            identityId = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
            bootId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
        )

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

    /** 组装一个「控制面会下发 [values]」的协调器。 */
    private fun coordinator(
        values: Map<String, String>,
        view: EffectiveConfigView,
        adapter: ManualAsyncAdapter,
        dataDir: Path,
    ): PushedConfigCoordinator =
        PushedConfigCoordinator(
            BeaconApiClient(FixedSettingsTransport(values), TestJsonCodec, settings()),
            PushedConfigStore(dataDir, TestJsonCodec),
            view,
            adapter,
        )

    // ===================== §A reload 并发位下发（poolSize） =====================

    /**
     * 下发合法并发位时应真落到派发器上。
     *
     * 这是「Web 改并发 → agent 生效」闭环里**唯一可断言处**：值必须穿过
     * store → 键级防护 → coordinator → 视图 → 派发器，任何一环断掉这里就停在初始值。
     */
    @Test
    fun `下发合法并发位应落到派发器`() {
        val adapter = ManualAsyncAdapter()
        val view = EffectiveConfigView(EffectiveConfigStore(), reloadTimeoutMs = 30_000L)
        try {
            val coordinator =
                coordinator(mapOf(PushedConfigCoordinator.KEY_RELOAD_POOL_SIZE to "8"), view, adapter, tempDir())
            assertTrue(coordinator.pullAndApply(identity()), "拉取应成功")
            assertEquals(8, view.effectiveReloadPoolSize, "下发的并发位应已生效")
            assertTrue(
                adapter.infos.any { it.contains("reload 并发位") },
                "应用后应有一条可追溯的 info，实际：${adapter.infos}",
            )
        } finally {
            view.close()
        }
    }

    /**
     * **非法值必须保留旧值并告警**（护栏「不得假报成功」）。
     *
     * 静默套用默认值是最坏的形态：运维在 Web 上看到"已下发"，实际容量却与他填的无关。
     * 故既断言容量不变，也断言**确实有 warn 说明为什么没生效**、且没有把它说成"已应用"。
     */
    @Test
    fun `下发非法并发位应告警并保留旧值`() {
        val adapter = ManualAsyncAdapter()
        val view = EffectiveConfigView(EffectiveConfigStore(), reloadTimeoutMs = 30_000L, reloadPoolSize = 16)
        try {
            val coordinator =
                coordinator(mapOf(PushedConfigCoordinator.KEY_RELOAD_POOL_SIZE to "很多"), view, adapter, tempDir())
            assertTrue(coordinator.pullAndApply(identity()), "拉取应成功")

            assertEquals(16, view.effectiveReloadPoolSize, "非法值不得改变既有并发位")
            assertTrue(
                adapter.warns.any { it.contains(PushedConfigCoordinator.KEY_RELOAD_POOL_SIZE) && it.contains("不是合法整数") },
                "非法值必须告警说明原因，实际告警：${adapter.warns}",
            )
            assertTrue(
                adapter.infos.none { it.contains("reload 并发位") },
                "非法值不得打「已应用」日志（否则等于把没生效说成生效），实际：${adapter.infos}",
            )
        } finally {
            view.close()
        }
    }

    /**
     * 越界值收口到 [1, 256] 并告警：`9999` 不能照原值建池。
     *
     * 上下界都要验：上界防「千级线程」把 MC 进程拖垮；下界防 0/负数让线程池构造直接抛异常。
     */
    @Test
    fun `下发越界并发位应收口到区间并告警`() {
        val adapter = ManualAsyncAdapter()
        val view = EffectiveConfigView(EffectiveConfigStore(), reloadTimeoutMs = 30_000L)
        try {
            val tooBig =
                coordinator(mapOf(PushedConfigCoordinator.KEY_RELOAD_POOL_SIZE to "9999"), view, adapter, tempDir())
            assertTrue(tooBig.pullAndApply(identity()), "拉取应成功")
            assertEquals(
                PushedConfigCoordinator.MAX_RELOAD_POOL_SIZE,
                view.effectiveReloadPoolSize,
                "上界应收口为 256",
            )
            assertTrue(
                adapter.warns.any { it.contains("超出合理范围") },
                "越界值应告警说明已收口，实际告警：${adapter.warns}",
            )

            val tooSmall =
                coordinator(mapOf(PushedConfigCoordinator.KEY_RELOAD_POOL_SIZE to "0"), view, adapter, tempDir())
            assertTrue(tooSmall.pullAndApply(identity()), "拉取应成功")
            assertEquals(
                PushedConfigCoordinator.MIN_RELOAD_POOL_SIZE,
                view.effectiveReloadPoolSize,
                "下界应收口为 1",
            )
        } finally {
            view.close()
        }
    }

    /** 未下发该键时不得动既有并发位（加性演进：老控制面 / 未配置的服照样工作）。 */
    @Test
    fun `未下发并发位时保持既有值`() {
        val adapter = ManualAsyncAdapter()
        val view = EffectiveConfigView(EffectiveConfigStore(), reloadTimeoutMs = 30_000L, reloadPoolSize = 32)
        try {
            val coordinator = coordinator(mapOf("timing.request-timeout-ms" to "5000"), view, adapter, tempDir())
            assertTrue(coordinator.pullAndApply(identity()), "拉取应成功")
            assertEquals(32, view.effectiveReloadPoolSize, "未下发该键时应保持 config.yml / 默认值")
            assertTrue(
                adapter.warns.none { it.contains(PushedConfigCoordinator.KEY_RELOAD_POOL_SIZE) },
                "未下发时不该出现与该键相关的告警，实际：${adapter.warns}",
            )
        } finally {
            view.close()
        }
    }

    /** 独立的临时数据目录：各用例互不共享下发缓存。 */
    private fun tempDir(): Path = Files.createTempDirectory("pushed-config-pool-size")

    // ===================== §E 重载超时下发 / 断连缓存 / 删项回退 / 防重入（E 组） =====================

    /** 固定返回 [statusCode] 的传输层：非 200 等价「这一轮没拉到」（不抛异常）。 */
    private class StatusSettingsTransport(
        private val statusCode: Int,
    ) : HttpTransport {
        override fun execute(request: HttpRequest): HttpResponse = HttpResponse(statusCode, "{}")
    }

    /** 直接抛异常的传输层：模拟网络层炸掉（[BeaconApiClient.exec] 会兜住并折算成拉取失败）。 */
    private class ThrowingSettingsTransport : HttpTransport {
        override fun execute(request: HttpRequest): HttpResponse = error("连接被重置")
    }

    /**
     * 可在两轮之间改写下发内容的传输层。
     *
     * 用于「同一个协调器连续几轮拿到不同配置」——生产里就是这一形态（一个协调器 + 30s 周期
     * 拉取）。每轮都新建协调器会让它的「已应用快照」从空开始，从而掩盖
     * 「缓存是合并还是整体替换」这类语义差异（实测：合并实现能骗过每轮新建的写法）。
     */
    private class MutableSettingsTransport(
        var values: Map<String, String>,
    ) : HttpTransport {
        override fun execute(request: HttpRequest): HttpResponse = HttpResponse(200, settingsBody(values))
    }

    /**
     * 会挂住请求的传输层：把「一轮拉取尚未结束」这个状态**确定性地**摆出来（防重入断言）。
     *
     * [release] 带 2 秒超时是安全阀：即便断言失败也不让测试永久挂住。
     */
    private class GatedSettingsTransport(
        values: Map<String, String>,
    ) : HttpTransport {
        private val body = settingsBody(values)

        val calls = AtomicInteger(0)
        val entered = CountDownLatch(1)
        val release = CountDownLatch(1)

        override fun execute(request: HttpRequest): HttpResponse {
            calls.incrementAndGet()
            entered.countDown()
            release.await(2, TimeUnit.SECONDS)
            return HttpResponse(200, body)
        }
    }

    /**
     * 跑满指定时长的假监听器。
     *
     * 视图/派发器**没有**重载超时的只读口（预算只在派发内部读一次），所以「下发值有没有真的
     * 落到派发器」只能靠行为判断：同一个慢插件在旧预算下必须超时、在新预算下必须成功。
     */
    private class SlowListener(
        private val blockMs: Long,
    ) : ConfigChangeListener {
        override fun onConfigChanged(
            changedDataIds: Set<String>,
            newMd5: String,
        ) {
            val deadline = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(blockMs)
            while (true) {
                val remainNanos = deadline - System.nanoTime()
                if (remainNanos <= 0L) {
                    return
                }
                try {
                    // 至少睡 1ms：Thread.sleep(0) 会立刻返回，把循环变成忙等。
                    Thread.sleep(TimeUnit.NANOSECONDS.toMillis(remainNanos).coerceAtLeast(1L))
                } catch (_: InterruptedException) {
                    // 刻意忽略：超时会中断插件线程，而这里要模拟的正是「中断也拦不住它跑满」。
                }
            }
        }
    }

    /** 记录执行线程的监听器：用于区分「走派发线程池」与「内联执行」。 */
    private class ThreadRecordingListener(
        private val ranOn: AtomicReference<Thread>,
    ) : ConfigChangeListener {
        override fun onConfigChanged(
            changedDataIds: Set<String>,
            newMd5: String,
        ) {
            ranOn.set(Thread.currentThread())
        }
    }

    /** `config.yml` 的最小替身：只回答 [values] 里写了的键，其余返回默认（= 该项没写）。 */
    private class YamlReader(
        private val values: Map<String, Any?>,
    ) : ConfigReader {
        override fun string(
            path: String,
            default: String,
        ): String = values[path] as? String ?: default

        override fun int(
            path: String,
            default: Int,
        ): Int = (values[path] as? Number)?.toInt() ?: default

        override fun long(
            path: String,
            default: Long,
        ): Long = (values[path] as? Number)?.toLong() ?: default

        override fun double(
            path: String,
            default: Double,
        ): Double = (values[path] as? Number)?.toDouble() ?: default

        override fun boolean(
            path: String,
            default: Boolean,
        ): Boolean = values[path] as? Boolean ?: default

        override fun stringList(path: String): List<String> = (values[path] as? List<*>)?.filterIsInstance<String>() ?: emptyList()

        override fun keys(path: String): Set<String> = emptySet()
    }

    /** §E 用的解析器：只读本地缓存与 config.yml、**不联网**——正是「断连时 agent 拿到的实际参数」。 */
    private fun resolver(
        path: Path,
        local: ConfigReader,
    ): EffectiveSettingsResolver =
        EffectiveSettingsResolver(
            localReader = local,
            pushedStore = PushedConfigStore(path, TestJsonCodec),
            credentialsStore = AgentCredentialsStore(path, TestJsonCodec),
            warn = {},
            info = {},
        )

    /** 只关心「值有没有落地」的视图：§E 的落地口，断言走 resolver / 行为，不看视图本身。 */
    private fun sinkView(): EffectiveConfigView = EffectiveConfigView(EffectiveConfigStore(), reloadTimeoutMs = 30_000L)

    /**
     * **正常下发**：控制面下发 `delivery.config-reload-timeout-ms=20000` → 真的落到派发器，
     * 并且落盘到缓存（断连后重启仍要用这份值）。
     *
     * 两处断言各锁一个环：
     *  - 行为断言（阻塞 300ms 的插件在 20000ms 预算下必须成功）锁「值穿过了
     *    store → 键级防护 → 协调器 → 视图 → 派发器」这条链；
     *  - 缓存断言锁「落盘」，否则 FR-277 #2 的断连可用根本无从谈起。
     */
    @Test
    fun `下发合法重载超时应落到派发器并落盘`() {
        val adapter = ManualAsyncAdapter()
        // 初始预算 100ms：这是「改之前」的实际预算，下发 20000 之后必须不再按它判定。
        val view = EffectiveConfigView(EffectiveConfigStore(), reloadTimeoutMs = 100L)
        val dir = tempDir()
        try {
            val coordinator = coordinator(mapOf(PushedConfigCoordinator.KEY_RELOAD_TIMEOUT_MS to "20000"), view, adapter, dir)
            assertTrue(coordinator.pullAndApply(identity()), "拉取应成功")

            view.onChange(SlowListener(blockMs = 300L))
            val outcome = view.fireChanged(setOf("liveops/schedule.json"), "md5-new").outcomes.single()

            assertTrue(outcome.ok, "下发 20000ms 后阻塞 300ms 的插件不该超时（值没落到派发器？），实际：${outcome.reason}")
            assertFalse(outcome.timeout, "不该被判超时")
            assertTrue(
                adapter.infos.any { it.contains("已应用下发的重载超时") && it.contains("20000") },
                "应用后应有一条可追溯的 info，实际：${adapter.infos}",
            )

            val persisted = PushedConfigStore(dir, TestJsonCodec).load()
            assertEquals(
                "20000",
                persisted.values[PushedConfigCoordinator.KEY_RELOAD_TIMEOUT_MS],
                "下发值必须落盘，断连 / 重启后仍要用它工作",
            )
            assertEquals("prod", persisted.namespace, "缓存的 namespace 应来自控制面的认定")
        } finally {
            view.close()
        }
    }

    /**
     * **非法值必须保留旧值并告警**（不得套用任何默认值）。
     *
     * 静默改成默认值是最坏的形态：运维在 Web 上看到「已下发」，实际预算却与他填的无关，
     * 而重载超时正是「插件卡死」的唯一定界，改错等于悄悄放掉保护。
     * 故既断言行为不变（仍按 100ms 判超时），也断言没有把它说成「已应用」。
     */
    @Test
    fun `下发非法重载超时应告警并保留旧值`() {
        val adapter = ManualAsyncAdapter()
        val view = EffectiveConfigView(EffectiveConfigStore(), reloadTimeoutMs = 100L)
        try {
            val coordinator =
                coordinator(mapOf(PushedConfigCoordinator.KEY_RELOAD_TIMEOUT_MS to "abc"), view, adapter, tempDir())
            assertTrue(coordinator.pullAndApply(identity()), "拉取应成功（拉取成功 ≠ 值合法）")

            view.onChange(SlowListener(blockMs = 300L))
            val outcome = view.fireChanged(setOf("liveops/schedule.json"), "md5-new").outcomes.single()

            assertTrue(outcome.timeout, "非法值不得改变既有预算（仍应按 100ms 判超时），实际：${outcome.reason}")
            assertTrue(
                adapter.warns.any { it.contains(PushedConfigCoordinator.KEY_RELOAD_TIMEOUT_MS) && it.contains("不是合法整数") },
                "非法值必须告警说明原因，实际告警：${adapter.warns}",
            )
            assertTrue(
                adapter.infos.none { it.contains("已应用下发的重载超时") },
                "非法值不得打「已应用」日志（否则等于把没生效说成生效），实际：${adapter.infos}",
            )
        } finally {
            view.close()
        }
    }

    /**
     * 越界值**收口到区间并告警**：`-1` 收口为 0（内联执行），超大值收口为 1 小时。
     *
     * 与「非法值」刻意不同：能解析成整数、只是超出合理范围时，实现选择收口而
     * 不是丢弃——直接丢弃会让「填了 -1」变成一个既没生效也没说清的状态。收口必须
     * 留痕（warn 里带出生效值），否则「填了 9999」在日志里完全消失。
     */
    @Test
    fun `下发越界重载超时应收口到区间并告警`() {
        val adapter = ManualAsyncAdapter()
        val view = EffectiveConfigView(EffectiveConfigStore(), reloadTimeoutMs = 30_000L)
        try {
            val negative =
                coordinator(mapOf(PushedConfigCoordinator.KEY_RELOAD_TIMEOUT_MS to "-1"), view, adapter, tempDir())
            assertTrue(negative.pullAndApply(identity()), "拉取应成功")

            // 0 = 不设超时、就地内联执行（AgentBootstrap 侧同一口径），故断言「在调用线程上跑」。
            val ranOn = AtomicReference<Thread>()
            view.onChange(ThreadRecordingListener(ranOn))
            view.fireChanged(setOf("liveops/schedule.json"), "md5-new")
            assertSame(Thread.currentThread(), ranOn.get(), "-1 应被收口为 0（内联执行）")
            assertTrue(
                adapter.warns.any { it.contains("超出合理范围") && it.contains("已收口为 0") },
                "负数应收口为 0 并在告警里带出生效值，实际告警：${adapter.warns}",
            )

            val tooBig =
                coordinator(mapOf(PushedConfigCoordinator.KEY_RELOAD_TIMEOUT_MS to "99999999"), view, adapter, tempDir())
            assertTrue(tooBig.pullAndApply(identity()), "拉取应成功")
            assertTrue(
                adapter.warns.any { it.contains("已收口为 ${PushedConfigCoordinator.MAX_RELOAD_TIMEOUT_MS}") },
                "超大值应收口为 ${PushedConfigCoordinator.MAX_RELOAD_TIMEOUT_MS}，实际告警：${adapter.warns}",
            )
        } finally {
            view.close()
        }
    }

    /**
     * **断连用缓存（FR-277 #2）**：拉取失败时沿用上次缓存，绝不退回代码默认。
     *
     * 「控制面一抖，全部 agent 回到出厂设置」对本项尤其致命：预算从 20000 退回 30000（或 0）
     * 是**无声**的——没有任何日志能告诉你保护被换掉了。
     *
     * 两种失败形态都验（非 200 / 传输层抛异常），并断言生效值仍等于缓存里的 20000/8
     * ——那是代码默认（30000/64）之外的数字，能直接区分「用了缓存」与「退回默认」。
     */
    @Test
    fun `拉取失败时沿用缓存值不退回代码默认`() {
        val dir = tempDir()
        val warmupView = sinkView()
        try {
            val warmup =
                coordinator(
                    mapOf(
                        PushedConfigCoordinator.KEY_RELOAD_TIMEOUT_MS to "20000",
                        PushedConfigCoordinator.KEY_RELOAD_POOL_SIZE to "8",
                    ),
                    warmupView,
                    ManualAsyncAdapter(),
                    dir,
                )
            assertTrue(warmup.pullAndApply(identity()), "预热拉取应成功")
        } finally {
            warmupView.close()
        }

        val brokenTransports: List<HttpTransport> = listOf(StatusSettingsTransport(503), ThrowingSettingsTransport())
        for (broken in brokenTransports) {
            val adapter = ManualAsyncAdapter()
            val view = sinkView()
            try {
                val offline =
                    PushedConfigCoordinator(
                        BeaconApiClient(broken, TestJsonCodec, settings()),
                        PushedConfigStore(dir, TestJsonCodec),
                        view,
                        adapter,
                    )
                assertFalse(offline.pullAndApply(identity()), "失败应如实返回 false")

                val cached = PushedConfigStore(dir, TestJsonCodec).load()
                assertEquals(
                    "20000",
                    cached.values[PushedConfigCoordinator.KEY_RELOAD_TIMEOUT_MS],
                    "拉取失败不得丢掉已下发的超时",
                )
                assertEquals("8", cached.values[PushedConfigCoordinator.KEY_RELOAD_POOL_SIZE], "拉取失败不得丢掉已下发的并发位")

                val resolved = resolver(dir, YamlReader(emptyMap())).resolve()
                assertEquals(20_000L, resolved.configReloadTimeoutMs, "断连时必须用缓存值，不得退回代码默认 30000")
                assertEquals(8, resolved.configReloadPoolSize, "断连时必须用缓存值，不得退回代码默认 64")
                assertTrue(
                    adapter.infos.none { it.contains("已应用下发的重载超时") },
                    "拉取失败不该「应用」任何值，实际：${adapter.infos}",
                )
            } finally {
                view.close()
            }
        }
    }

    /**
     * **删项回退（FR-277 #3）**：下发项被移除 → 回退到 config.yml → 再回退到代码默认。
     *
     * 关键在「整体替换而非合并」：若缓存是合并语义，被删掉的那一项会永远留在
     * `pushed-config.json` 里，Web 上删了半年、agent 还在按它工作——这种「幽灵配置」几乎无法排查。
     *
     * ## 为什么必须是**同一个协调器**连拉两轮
     *
     * 合并语义只能在协调器已有「已应用快照」时才显形。每轮新建协调器（快照从空开始）会让
     * 合并实现与替换实现表现得一模一样——实测确认过：那样写这条用例抓不到合并回归。
     * 生产里就是一个协调器 + 周期拉取，故这里用可变传输层 + `primeFromCache()` 还原同一形态。
     */
    @Test
    fun `下发项被移除后回退到 configyml 再回退到代码默认`() {
        val dir = tempDir()
        val adapter = ManualAsyncAdapter()
        val view = sinkView()
        // config.yml 显式配了 5000：这是第二层兜底。
        val localYaml = YamlReader(mapOf(PushedConfigCoordinator.KEY_RELOAD_TIMEOUT_MS to 5_000L))
        val transport = MutableSettingsTransport(mapOf(PushedConfigCoordinator.KEY_RELOAD_TIMEOUT_MS to "20000"))
        try {
            val store = PushedConfigStore(dir, TestJsonCodec)
            val coordinator = PushedConfigCoordinator(BeaconApiClient(transport, TestJsonCodec, settings()), store, view, adapter)
            // 与生产装配一致（forAssembly 会 primeFromCache）：重启后先认缓存，再开始周期拉取。
            coordinator.primeFromCache()

            assertTrue(coordinator.pullAndApply(identity()), "首次下发应成功")
            assertEquals(
                20_000L,
                resolver(dir, localYaml).resolve().configReloadTimeoutMs,
                "下发值应优先于 config.yml",
            )

            // 控制面把该项删掉：这一轮下发里没有它。
            transport.values = emptyMap()
            assertTrue(coordinator.pullAndApply(identity()), "空下发也应算拉取成功")
            assertTrue(
                store.load().values.isEmpty(),
                "缓存应整体替换，被删掉的项不得残留（否则就是幽灵配置）",
            )
            assertEquals(
                5_000L,
                resolver(dir, localYaml).resolve().configReloadTimeoutMs,
                "下发项被删后应回退到 config.yml",
            )
            assertEquals(
                30_000L,
                resolver(dir, YamlReader(emptyMap())).resolve().configReloadTimeoutMs,
                "config.yml 也没写时应回退到代码默认 30000",
            )
        } finally {
            view.close()
        }
    }

    /**
     * **防重入**：一轮拉取未结束时再调 `pullAndApply` 必须被挡下，且**不产生第二次网络请求**。
     *
     * 周期任务（30s 一轮）与控制面触发的立即拉取可能重叠；没有这道闸，慢网络下会同时在
     * 排队几轮相同的请求，且后到的一轮可能用更旧的数据覆盖刚写入的缓存。
     *
     * 只断言「返回 false」不够——拉取失败也返回 false，两者必须靠**请求次数**区分。
     * 末尾再拉一次，锁住「上一轮结束后闸门会复位」，否则协调器就此永久停摆。
     */
    @Test
    fun `并发拉取时只有一次真正执行`() {
        val gated = GatedSettingsTransport(mapOf(PushedConfigCoordinator.KEY_RELOAD_TIMEOUT_MS to "20000"))
        val view = sinkView()
        try {
            val coordinator =
                PushedConfigCoordinator(
                    BeaconApiClient(gated, TestJsonCodec, settings()),
                    PushedConfigStore(tempDir(), TestJsonCodec),
                    view,
                    ManualAsyncAdapter(),
                )
            val firstResult = AtomicReference<Boolean>()
            val first = Thread { firstResult.set(coordinator.pullAndApply(identity())) }
            first.start()
            try {
                assertTrue(gated.entered.await(5, TimeUnit.SECONDS), "首轮拉取应已进入传输层")

                // 轮次尚未结束：再拉一次必须被 inFlight 挡下。
                assertFalse(coordinator.pullAndApply(identity()), "重入应被挡下并返回 false")
                assertEquals(1, gated.calls.get(), "防重入失效会打出第二次请求")
            } finally {
                gated.release.countDown()
            }
            first.join(10_000)
            assertEquals(true, firstResult.get(), "首轮应成功")

            // 首轮已结束：闸门必须复位，否则协调器就此停摆（周期拉取全部变成空转）。
            assertTrue(coordinator.pullAndApply(identity()), "上一轮结束后应能再次拉取")
            assertEquals(2, gated.calls.get(), "复位后这一次应真的发出请求")
        } finally {
            view.close()
        }
    }
}

/** 控制面 settings 响应体：只有 values 随用例变化，其余字段固定。 */
private fun settingsBody(values: Map<String, String>): String =
    TestJsonCodec.encode(
        mapOf(
            "values" to values,
            "namespace" to "prod",
            "serverId" to "lobby-1",
            "rejectedKeys" to emptyList<String>(),
        ),
    )
