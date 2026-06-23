package top.wcpe.beacon.agent.core.metrics

import java.net.Socket
import java.net.SocketAddress
import java.util.concurrent.CountDownLatch
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicLong

/**
 * 后端可达性 TCP 连接探测（FR-34，[ADR-0035] 取代 ADR-0025 的 MC status-ping 探测机制）。
 *
 * 对每个后端地址并发发起一次带超时的阻塞 TCP 连接：连上即可达（RTT=连接耗时毫秒），
 * 连接被拒 / 超时 / 无路由即不可达。相比 MC server-list-ping 更稳健——**不依赖后端是否应答
 * status ping**：代理后端常因开启转发（bungeecord / 现代 proxy 模式）对未携带转发握手的 status
 * ping 不作应答，但其 TCP 端口可连即代理可路由，TCP 连接才是「可达」的真实信号（真机实测：
 * Paper 后端 TCP 可连但 status ping 超时，致旧 ping 方案对在线后端误报不可达）。
 *
 * 平台无关纯 JDK Socket，便于单测；连接在独立守护线程池执行、**绝不占用 MC 主线程**（守架构不变量 #5），
 * 调用方（agent 既有 async 上报线程）仅有界等待汇总。探测结果交 [BackendReachability.summarize] 聚合。
 */
object TcpBackendProbe {

    /** 后端探测专用守护线程池：逐后端并发阻塞连接、互不叠加超时；空闲线程自动回收。 */
    private val executor: ExecutorService = Executors.newCachedThreadPool { r ->
        Thread(r, "beacon-backend-probe").apply { isDaemon = true }
    }

    /**
     * 并发探测一组后端地址，有界等待汇总每后端探测结果。
     *
     * @param addresses 后端地址集合；空集返回空结果
     * @param connectTimeoutMs 单后端 TCP 连接超时（毫秒）；总等待略大于此值（少数慢后端不叠加超时）
     */
    fun probe(addresses: List<SocketAddress>, connectTimeoutMs: Long): List<BackendReachability.PingProbe> {
        if (addresses.isEmpty()) return emptyList()
        val latch = CountDownLatch(addresses.size)
        val slots = addresses.map { Slot() }
        addresses.forEachIndexed { idx, addr ->
            val slot = slots[idx]
            executor.execute {
                val startNanos = System.nanoTime()
                try {
                    Socket().use { sock ->
                        // 阻塞连接，超时即抛 SocketTimeoutException；连接被拒抛 ConnectException。
                        sock.connect(addr, connectTimeoutMs.toInt())
                        slot.reachable.set(true)
                        slot.latencyMs.set((System.nanoTime() - startNanos) / 1_000_000L)
                    }
                } catch (e: Exception) {
                    // 连接被拒 / 超时 / 无路由 / 地址非法 → 不可达，保持 reachable=false，不抛、不刷屏。
                } finally {
                    latch.countDown()
                }
            }
        }
        try {
            // 总等待略大于单连接超时：全部连接并发进行，慢后端不叠加；未回的按不可达计。
            latch.await(connectTimeoutMs + 500L, TimeUnit.MILLISECONDS)
        } catch (e: InterruptedException) {
            Thread.currentThread().interrupt()
        }
        return slots.map { BackendReachability.PingProbe(reachable = it.reachable.get(), latencyMs = it.latencyMs.get()) }
    }

    /** 单后端探测结果槽：连接线程写、采集线程读，用原子量隔离可见性。 */
    private class Slot {
        val reachable = AtomicBoolean(false)
        val latencyMs = AtomicLong(0L)
    }
}
