package top.wcpe.beacon.e2e

import taboolib.common.platform.function.getDataFolder
import taboolib.common.platform.function.info
import taboolib.common.platform.function.submit
import top.wcpe.beacon.agent.api.BeaconAgentProvider
import top.wcpe.beacon.agent.api.DiscoveryQuery
import top.wcpe.beacon.agent.api.ListenerHandle
import top.wcpe.beacon.agent.api.ServiceInstance
import java.io.File
import java.util.concurrent.atomic.AtomicInteger

/**
 * FR-29 / FR-30 / FR-31 服务发现与拓扑订阅端到端探针（Bukkit 壳）。
 *
 * 作为「业务插件」经 agent 的纯 Java 只读门面 `BeaconAgentProvider.get().discovery()` 验证三件事：
 *  1. **发现查询**：`query(DiscoveryQuery)` 取到本 namespace 的可见实例，字段与 admin 实例视图可比；
 *  2. **拓扑订阅**：`watch(TopologyListener)` 在控制面推送 `topology-changed` 时被回调；
 *     回调后重新 `query()` 必须反映**新**拓扑——契约约定回调只通知「变了」，不搬实例数据。
 *  3. **玩家名册**：`roster()` / `rosterInZone()` 返回玩家名→serverId；无人在线时返回**空 Map 而非报错**
 *     （fail-static 降级形态）。
 *
 * ## 为什么主断言用 query() 而不是 instancesInGroup
 *
 * `instancesInGroup(group)` / `instancesInZone(group, zone)` 都要求传入 group 名，而 group 是
 * **控制面解析回填**的（agent 自身不声明 group）。实例刚注册时 group 可能尚未回填，
 * 按 group 查询必然空——这是合法时序状态，不是发现能力缺陷。故主观测用不带约束的 `query()`，
 * 它等价「本 namespace 内可见实例」，不依赖任何回填时序。
 * group/zone 维度仍照常观测：group 有值才查，供外部区分「维度未就绪」与「查询失败」。
 *
 * 观测写到 `e2e-discovery.log`，单行格式沿用 [E2EObservation]，外部驱动按 `|` 分割解析。
 * 每行第 2 段（source）的取值即该行的语义类型：
 *  - `DISCOVERY_QUERY`   不带约束的 query 结果（主断言来源）
 *  - `DISCOVERY_GROUP`   小组查询结果（group 已回填时才产）
 *  - `DISCOVERY_ZONE`    小区查询结果（group+zone 都可用时才产）
 *  - `TOPOLOGY_CHANGED`  拓扑回调触发（第 4 段为累计回调次数）
 *  - `TOPOLOGY_AFTER`    回调后重查的拓扑摘要
 *  - `ROSTER_ALL`        全量名册快照
 *  - `ROSTER_ZONE`       指定小区名册快照
 *  - `DISCOVERY_ERROR`   门面调用抛异常（fail-static 契约下不应出现）
 *
 * 由环境变量 `BEACON_E2E_DISCOVERY` 门控（非空才启用）。
 */
object DiscoveryE2EProbe {
    /** 标记文件名：外部驱动据此断言。 */
    private const val OBSERVATION_FILE = "e2e-discovery.log"

    /** 轮询周期（tick，20 tick/秒）：约每 2 秒刷新一次发现/名册观测。 */
    private const val POLL_INTERVAL_TICKS = 40L

    /** 目标小区名（环境注入）；空则不产出 zone 维度观测。 */
    private val zone: String = System.getenv("BEACON_E2E_SCHED_ZONE") ?: ""

    /** 目标小组名（环境注入）；空则用 agent 自身 identity 的 group 兜底。 */
    private val group: String = System.getenv("BEACON_E2E_DISCOVERY_GROUP") ?: ""

    /** 拓扑回调累计次数：外部据此断言「确实回调过」而不是只看到一条静态观测。 */
    private val topologyCallbacks = AtomicInteger(0)

    /** watch 注销句柄；DISABLE 时释放。 */
    private var watchHandle: ListenerHandle? = null

    /** 是否已启动（避免重复注册 watch）。 */
    @Volatile
    private var started = false

    /**
     * 启动探针：清旧标记 → 注册拓扑监听 → 启异步轮询刷新发现与名册观测。
     * 由 BeaconE2EBukkit 在 ENABLE 阶段调用。
     */
    fun start() {
        if (System.getenv("BEACON_E2E_DISCOVERY").isNullOrBlank()) {
            info("未配置 BEACON_E2E_DISCOVERY，跳过服务发现探针")
            return
        }
        val markFile = File(getDataFolder(), OBSERVATION_FILE)
        // 清空上轮残留，保证每次 run 的标记文件只含本轮观测。
        if (markFile.exists()) {
            markFile.delete()
        }
        topologyCallbacks.set(0)
        started = true
        info(
            "Beacon E2E 服务发现探针已启用，group=${group.ifBlank { "（用自身）" }}，" +
                "zone=${zone.ifBlank { "（不启用）" }}，标记文件=${markFile.absolutePath}",
        )

        // 拓扑订阅：agent 未就绪时静默跳过，由轮询兜底补注册。
        registerTopologyWatch(markFile)

        submit(async = true, delay = POLL_INTERVAL_TICKS, period = POLL_INTERVAL_TICKS) {
            registerTopologyWatch(markFile)
            probeOnce(markFile)
        }
    }

    /** 停止探针：释放拓扑监听（由 BeaconE2EBukkit 在 DISABLE 阶段调用）。 */
    fun stop() {
        watchHandle?.remove()
        watchHandle = null
        started = false
    }

    /**
     * 注册拓扑监听。
     *
     * 回调里**只记一次观测并重查**：契约明确回调不携带实例数据，插件必须自己重新 query，
     * 故这里在回调内直接取一次最新拓扑摘要，供外部断言「回调后看到的是新拓扑」。
     */
    private fun registerTopologyWatch(markFile: File) {
        if (!started || watchHandle != null || !BeaconAgentProvider.isAvailable()) {
            return
        }
        watchHandle =
            BeaconAgentProvider.get().discovery().watch {
                val seq = topologyCallbacks.incrementAndGet()
                E2EObservation.append(markFile, "TOPOLOGY_CHANGED", "watch", seq.toString(), "回调第 $seq 次")
                // 回调后重查：把此刻门面看到的拓扑摘要落盘，外部据此断言新拓扑已可见。
                runCatching { topologySummary() }
                    .onSuccess {
                        E2EObservation.append(markFile, "TOPOLOGY_AFTER", "watch", seq.toString(), it)
                    }.onFailure {
                        E2EObservation.append(
                            markFile,
                            "DISCOVERY_ERROR",
                            "TOPOLOGY_AFTER",
                            "-",
                            "err=${it.javaClass.simpleName}: ${it.message}",
                        )
                    }
            }
        info("Beacon E2E 已注册拓扑变更监听")
    }

    /** 取一次发现 + 名册并把各维度写为观测行；任何异常都写成 DISCOVERY_ERROR 行。 */
    private fun probeOnce(markFile: File) {
        if (!BeaconAgentProvider.isAvailable()) {
            return
        }
        val discovery = BeaconAgentProvider.get().discovery()
        val self = BeaconAgentProvider.get().identity()
        // group() 返回 Optional：未指派时为空，必须显式解包，不能直接当 String 用。
        val effectiveGroup = group.ifBlank { self.group().orElse("") }

        runCatching {
            // 主观测：不带任何约束的 query，等价「本 namespace 可见实例」，不依赖 group 回填时序。
            E2EObservation.append(
                markFile,
                "DISCOVERY_QUERY",
                self.serverId(),
                "-",
                render(discovery.query(DiscoveryQuery.builder().build())),
            )
            if (effectiveGroup.isNotBlank()) {
                E2EObservation.append(
                    markFile,
                    "DISCOVERY_GROUP",
                    effectiveGroup,
                    "-",
                    render(discovery.instancesInGroup(effectiveGroup)),
                )
            }
            if (effectiveGroup.isNotBlank() && zone.isNotBlank()) {
                E2EObservation.append(
                    markFile,
                    "DISCOVERY_ZONE",
                    zone,
                    "-",
                    render(discovery.instancesInZone(effectiveGroup, zone)),
                )
            }

            // 名册：无人在线时必须是空 Map（不是 null、不抛异常）——这是 fail-static 契约。
            E2EObservation.append(markFile, "ROSTER_ALL", "-", "-", renderRoster(discovery.roster()))
            if (effectiveGroup.isNotBlank() && zone.isNotBlank()) {
                E2EObservation.append(
                    markFile,
                    "ROSTER_ZONE",
                    zone,
                    "-",
                    renderRoster(discovery.rosterInZone(effectiveGroup, zone)),
                )
            }
        }.onFailure {
            E2EObservation.append(
                markFile,
                "DISCOVERY_ERROR",
                "probe",
                "-",
                "err=${it.javaClass.simpleName}: ${it.message}",
            )
        }
    }

    /** 把实例列表渲染为稳定排序的单行文本，便于外部逐字段比对（避免顺序抖动造成误判）。 */
    private fun render(instances: List<ServiceInstance>): String =
        instances
            .sortedBy { it.serverId() }
            .joinToString(";") { instance ->
                "serverId=${instance.serverId()}" +
                    ",role=${instance.role()}" +
                    ",group=${instance.group()}" +
                    ",zone=${instance.zone()}" +
                    ",address=${instance.address()}" +
                    ",capacity=${instance.capacity()}" +
                    ",status=${instance.status()}" +
                    ",playerCount=${instance.playerCount()}" +
                    ",metadata=${renderMap(instance.metadata())}"
            }

    /**
     * 把名册渲染为稳定排序的单行文本（玩家名=serverId）。
     *
     * 空名册显式标记为 EMPTY，与「取到了但渲染成空串」区分开——外部要能断言降级形态。
     */
    private fun renderRoster(roster: Map<String, String>): String {
        if (roster.isEmpty()) {
            return "EMPTY"
        }
        return roster.entries.sortedBy { it.key }.joinToString(";") { "${it.key}=${it.value}" }
    }

    /** 键值对渲染为稳定排序文本。 */
    private fun renderMap(map: Map<String, String>): String =
        if (map.isEmpty()) {
            "-"
        } else {
            map.entries.sortedBy { it.key }.joinToString(",") { "${it.key}=${it.value}" }
        }

    /**
     * 拓扑摘要：本服所在 group（或全量）的实例 serverId→zone 映射。
     *
     * 用摘要而非全量字段，是因为外部只需断言「换区后本服的 zone 变了」；摘要更短，
     * 落盘与解析都更稳。
     */
    private fun topologySummary(): String {
        val discovery = BeaconAgentProvider.get().discovery()
        val self = BeaconAgentProvider.get().identity()
        val effectiveGroup = group.ifBlank { self.group().orElse("") }
        val instances =
            if (effectiveGroup.isNotBlank()) {
                discovery.instancesInGroup(effectiveGroup)
            } else {
                // group 未回填时退回不带约束的 query：换区后本服的 zone 变化同样可见，
                // 避免因 group 时序未就绪而让拓扑断言整体失效。
                discovery.query(DiscoveryQuery.builder().build())
            }
        return if (instances.isEmpty()) {
            "EMPTY"
        } else {
            instances.sortedBy { it.serverId() }.joinToString(";") { "${it.serverId()}=${it.zone()}" }
        }
    }
}
