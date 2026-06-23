package top.wcpe.beacon.agent.core.metrics

import java.net.InetSocketAddress
import java.net.ServerSocket
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * TcpBackendProbe 单测（FR-34 / ADR-0035）：用真实 socket 验证 TCP 连接探测正确区分可达 / 不可达。
 *
 * 这是 ping→TCP 改造的核心防回归：旧 MC status-ping 对 TCP 可连但不应答 status 的后端误报不可达，
 * TCP 连接探测以「端口能否连上」为准，恰好规避该误报。
 */
class TcpBackendProbeTest {

    @Test
    fun `监听中的端口判可达、未监听端口判不可达`() {
        // 绑定随机空闲端口并监听 = 可达后端。
        val listening = ServerSocket(0)
        try {
            val reachable = InetSocketAddress("127.0.0.1", listening.localPort)
            // 取一个刚释放的端口作不可达：开后立即关，该端口大概率无人监听 → 连接被拒。
            val tmp = ServerSocket(0)
            val closedPort = tmp.localPort
            tmp.close()
            val unreachable = InetSocketAddress("127.0.0.1", closedPort)

            val probes = TcpBackendProbe.probe(listOf(reachable, unreachable), 1_000L)

            assertEquals(2, probes.size, "应逐后端返回一条探测结果")
            assertTrue(probes[0].reachable, "监听中的端口应判可达")
            assertTrue(probes[0].latencyMs >= 0L, "可达后端延迟应为非负毫秒")
            assertFalse(probes[1].reachable, "未监听端口应判不可达")
        } finally {
            listening.close()
        }
    }

    @Test
    fun `聚合可达性与延迟`() {
        val listening = ServerSocket(0)
        try {
            val reachable = InetSocketAddress("127.0.0.1", listening.localPort)
            val tmp = ServerSocket(0)
            val closedPort = tmp.localPort
            tmp.close()
            val unreachable = InetSocketAddress("127.0.0.1", closedPort)

            val reach = BackendReachability.summarize(
                TcpBackendProbe.probe(listOf(reachable, unreachable), 1_000L),
            )
            assertEquals(1, reach.up, "应有 1 个可达后端")
            assertEquals(2, reach.total, "后端总数应为 2")
            assertTrue(reach.avgLatencyMs >= 0.0, "有可达后端时平均延迟应为非负、非不可用哨兵")
        } finally {
            listening.close()
        }
    }

    @Test
    fun `空地址集返回空结果`() {
        assertTrue(TcpBackendProbe.probe(emptyList(), 1_000L).isEmpty(), "无后端应返回空结果")
    }
}
