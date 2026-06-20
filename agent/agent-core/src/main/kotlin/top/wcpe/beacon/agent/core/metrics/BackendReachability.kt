package top.wcpe.beacon.agent.core.metrics

/**
 * 后端可达性探测结果聚合（FR-34，平台无关纯逻辑，便于单测）。
 *
 * bc 壳逐后端发 `ServerInfo.ping` 异步探活，把每个后端的探测结果（可达 + RTT 毫秒，或不可达）
 * 汇成一组 [PingProbe] 后交本聚合器算 up / total / 平均延迟——平台仅负责发 ping 与有界等待，
 * 计数与求均值是无副作用纯函数。
 */
object BackendReachability {

    /**
     * 单个后端的 ping 探测结果。
     *
     * @param reachable 是否 ping 成功（在有界等待内拿到回调且无异常）
     * @param latencyMs 成功时的 RTT 毫秒；不可达时取值无意义（聚合只对 reachable 的样本取均值）
     */
    data class PingProbe(val reachable: Boolean, val latencyMs: Long)

    /**
     * 把一组后端探测结果聚合为 [ProxyMetrics] 的可达性三元（up / total / 平均延迟）。
     *
     * - total = 探测的后端总数；up = reachable 为 true 的个数。
     * - 平均延迟 = 全部可达后端 latencyMs 的均值；无任何可达后端时为 [ProxyMetrics.LATENCY_UNAVAILABLE]（-1.0）。
     * - 空输入（无配置后端）→ up=0 / total=0 / 延迟不可用。
     */
    fun summarize(probes: List<PingProbe>): Reachability {
        val total = probes.size
        var up = 0
        var sumLatency = 0L
        for (p in probes) {
            if (p.reachable) {
                up++
                sumLatency += p.latencyMs.coerceAtLeast(0L)
            }
        }
        val avg = if (up == 0) ProxyMetrics.LATENCY_UNAVAILABLE else sumLatency.toDouble() / up
        return Reachability(up = up, total = total, avgLatencyMs = avg)
    }

    /** 后端可达性聚合结果。 */
    data class Reachability(val up: Int, val total: Int, val avgLatencyMs: Double)
}
