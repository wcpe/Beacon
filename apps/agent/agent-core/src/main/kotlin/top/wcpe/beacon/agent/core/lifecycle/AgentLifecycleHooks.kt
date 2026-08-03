package top.wcpe.beacon.agent.core.lifecycle

import top.wcpe.beacon.agent.core.client.SelfHealth
import top.wcpe.beacon.agent.core.command.ReverseFetchExecutor
import top.wcpe.beacon.agent.core.filetree.FileTreeApplier
import top.wcpe.beacon.agent.core.metrics.ProxyMetricsProvider
import top.wcpe.beacon.agent.core.metrics.RuntimeMetrics
import top.wcpe.beacon.agent.core.metrics.RuntimeMetricsProvider
import top.wcpe.beacon.agent.core.override.OverrideSyncApplier
import top.wcpe.beacon.agent.core.scheduling.SchedulingRuntime

/** AgentLifecycle 的可选钩子与供给集合（向后兼容：缺省即旧行为——零指标 / 空集 / 不启用可选子系统）。 */
data class AgentLifecycleHooks(
    // 文件树对账器（通道B）：为 null 不启用文件树长轮询。
    val fileTreeApplier: FileTreeApplier? = null,
    // 三方覆盖对账器（FR-15）：为 null 不启用覆盖集长轮询。
    val overrideApplier: OverrideSyncApplier? = null,
    // 拓扑变更回调（FR-29）：收到 topology-changed 事件时触发，业务侧据此重查发现端点；为 null 不回调。
    val topologyListener: (() -> Unit)? = null,
    // 运行指标供给（FR-32）：上报时取当前一帧负载指标（人数 / TPS / 内存 / CPU）；
    // 默认零指标（向后兼容旧行为）；壳层注入平台采集实现以上报真值。
    val metricsProvider: RuntimeMetricsProvider = { RuntimeMetrics.ZERO },
    // 后端归属供给（FR-36）：注册/上报时取本机（仅 bc 代理）当前代理的后端子服 serverId 集合；
    // 默认空集（bukkit / 旧行为，不上报 backends）；bungee 壳层注入 ProxyServerDirectory 读取。
    val backendsProvider: () -> List<String> = { emptyList() },
    // BC 专属指标供给（FR-34）：上报时取本机（仅 bc 代理）当前一帧代理负载指标（连接 / 线程 / 运行时长 / 后端可达性·延迟）；
    // 默认 null（bukkit / 旧行为，不上报 proxy 段）；bungee 壳层注入平台采集实现。
    val proxyMetricsProvider: ProxyMetricsProvider = { null },
    // 反向抓取执行器（FR-39，见 ADR-0027）：收到 SSE command-pending 事件 / READY 对账时触发「拉命令→读 plugins→回传」；
    // 为 null 时不处理命令（向后兼容：未装配执行器的部署不开放反向抓取）。
    val reverseFetchExecutor: ReverseFetchExecutor? = null,
    // 调度候选刷新循环（FR-148）：注册成功时 start、停机时 stop、启动时 restoreSnapshot；
    // 为 null 时不启用（向后兼容既有测试；生产由 AgentAssembly 注入 SchedulingRefresher）。
    val schedulingRuntime: SchedulingRuntime? = null,
    // 自身健康回传 sink（FR-148）：转交给指标上报协调器，把 202 响应内 self 刷给调度门面；默认 no-op。
    val selfHealthSink: (SelfHealth?) -> Unit = {},
    // 文件资产索引周期扫描协调器（FR-163，见 ADR asset-manifest-sync-protocol）：注册成功时 start、停机时 stop；
    // 为 null 时不启用（assets 关闭 / 基目录无效 / 既有测试向后兼容），由 AgentAssembly 按 settings.assets.enabled 装配。
    val assetScan: AssetScanCoordinator? = null,
    // 权威身份被控制面禁用、拒绝、冲突或解绑时通知壳层撤销 Active runtime 对外门面。
    val authorityInvalidated: () -> Unit = {},
)
