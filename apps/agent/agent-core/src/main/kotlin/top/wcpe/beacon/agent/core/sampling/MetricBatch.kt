package top.wcpe.beacon.agent.core.sampling

/**
 * 5s 桶内的 CPU / 内存聚合（两种角色通用，FR-144 §3.1）。
 *
 * @param cpuPctAvg    桶内 CPU% 均值（仅对可用样本求均，全不可用为 -1）
 * @param cpuPctMax    桶内 CPU% 最大值（全不可用为 -1）
 * @param memUsedMbAvg 桶内已用堆 MB 均值
 * @param memMaxMb     桶内最大堆 MB（取桶内最大）
 */
data class LoadAgg(
    val cpuPctAvg: Double,
    val cpuPctMax: Double,
    val memUsedMbAvg: Double,
    val memMaxMb: Int,
)

/** 5s 桶聚合载荷（proxy / backend 差异部分，FR-144 §3.1）。 */
sealed interface BatchPayload

/**
 * backend 5s 桶聚合字段（FR-144 §3.1）。
 *
 * @param tpsAvg     桶内 TPS 均值
 * @param tpsMin     桶内 TPS 最小值（取保守值）
 * @param onlineAvg  桶内在线均值
 * @param onlineMax  桶内在线最大值
 * @param maxOnline  容量上限（桶内最大）
 */
data class BackendBatch(
    val tpsAvg: Double,
    val tpsMin: Double,
    val onlineAvg: Int,
    val onlineMax: Int,
    val maxOnline: Int,
) : BatchPayload

/**
 * proxy 5s 桶聚合字段（FR-144 §3.1）。
 *
 * @param connAvg          桶内连接数均值
 * @param connMax          桶内连接数最大值
 * @param backendUp        可达后端数（桶内最大）
 * @param backendTotal     配置后端总数（桶内最大）
 * @param backendRttMsAvg  可达后端 RTT 均值（仅对可用样本求均，全不可用为 -1）
 */
data class ProxyBatch(
    val connAvg: Int,
    val connMax: Int,
    val backendUp: Int,
    val backendTotal: Int,
    val backendRttMsAvg: Double,
) : BatchPayload

/**
 * 一台服务器一个 5s 批的**批内聚合**（FR-144 §3.1）——一行落 `metric_sample_YYYYMMDD` 的载体。
 *
 * `bucketStartMs = tsMs − tsMs % 5000`（agent 采样时钟），`sampleCount` 为桶内实际样本数（1~5）。
 * 控制面按 `(serverId, bucketStartMs)` 唯一键去重（补报 / 重放幂等）。
 *
 * @param bucketStartMs 5s 桶起点
 * @param sampleCount   桶内实际样本数（1~5）
 * @param load          CPU / 内存聚合
 * @param reportRttMs   桶内最后一条样本携带的上报 RTT（未知为 -1）
 * @param payload       proxy / backend 差异聚合字段
 */
data class MetricBatch(
    val bucketStartMs: Long,
    val sampleCount: Int,
    val load: LoadAgg,
    val reportRttMs: Int,
    val payload: BatchPayload,
)
