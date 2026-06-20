package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.BackoffSettings
import top.wcpe.beacon.agent.core.settings.FileTreeSettings
import top.wcpe.beacon.agent.core.settings.OverrideSettings
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.io.IOException
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * BeaconApiClient 连接级失败原因诊断单测：聚焦 register() 路径。
 *
 * 重点：
 * - exec 抛 IOException 时，RegisterOutcome.Failed.reason 应携带异常类名与原始消息子串，
 *   避免诊断完全黑盒；
 * - 一次成功调用必须清空 lastConnectFailure，下一次新失败不会回填上一次旧值，
 *   仅反映本次真实异常类名与消息。
 */
class BeaconApiClientConnectFailureTest {

    /** 编码不关心、解码用于 register 200 解析的最小 codec。 */
    private class StubCodec : JsonCodec {
        override fun encode(value: Any?): String = "encoded"

        // register 解析仅需基本字段；返回一份最小合法对象即可。
        override fun decode(json: String): Any? = mapOf(
            "instanceKey" to "prod/bc-1",
            "heartbeatIntervalSec" to 10,
            "ttlSec" to 30,
            "assigned" to false,
        )
    }

    /** 可编排响应 / 异常的可控 transport：按入队顺序依次抛或返。 */
    private class ScriptedTransport(private val steps: ArrayDeque<Step>) : HttpTransport {
        sealed class Step {
            data class Throw(val ex: Throwable) : Step()
            data class Return(val resp: HttpResponse) : Step()
        }

        override fun execute(request: HttpRequest): HttpResponse {
            val step = steps.removeFirst()
            return when (step) {
                is Step.Throw -> throw step.ex
                is Step.Return -> step.resp
            }
        }
    }

    private fun identity() = AgentIdentity(
        namespace = "prod",
        serverId = "bc-1",
        role = "bungee",
        groupHint = "area1",
        address = "127.0.0.1:25577",
        version = "1.0",
        capacity = 0,
        weight = 1,
        metadata = emptyMap(),
    )

    private fun settings() = AgentSettings(
        endpoints = listOf("http://localhost:18848"),
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

    @Test
    fun `register 连接失败时 reason 携带异常类名与消息`() {
        val transport = ScriptedTransport(
            ArrayDeque(
                listOf(
                    ScriptedTransport.Step.Throw(IOException("connection refused to localhost:18848")),
                ),
            ),
        )
        val client = BeaconApiClient(transport, StubCodec(), settings())

        val outcome = client.register(identity())

        // 失败分支：连接级异常应包装为 Failed，并把异常类名 + 原始消息带进 reason。
        val failed = outcome as? RegisterOutcome.Failed
            ?: error("期望 RegisterOutcome.Failed，实际：$outcome")
        assertTrue(
            "IOException" in failed.reason,
            "reason 应含异常类名 IOException，实际：${failed.reason}",
        )
        assertTrue(
            "connection refused" in failed.reason,
            "reason 应含原始异常消息子串，实际：${failed.reason}",
        )
    }

    @Test
    fun `register 成功后再失败的 reason 不会回填旧值`() {
        // 步序：失败 → 成功（清空 lastConnectFailure）→ 新失败。
        val transport = ScriptedTransport(
            ArrayDeque(
                listOf(
                    ScriptedTransport.Step.Throw(IOException("connection refused to localhost:18848")),
                    ScriptedTransport.Step.Return(HttpResponse(200, "{}")),
                    ScriptedTransport.Step.Throw(RuntimeException("boom")),
                ),
            ),
        )
        val client = BeaconApiClient(transport, StubCodec(), settings())

        // 第一次：失败，先把 lastConnectFailure 写入 IOException 信息。
        val firstFailed = client.register(identity()) as? RegisterOutcome.Failed
            ?: error("第一步期望 Failed")
        assertTrue("IOException" in firstFailed.reason)

        // 第二次：200 成功，应清空 lastConnectFailure。
        val success = client.register(identity())
        assertTrue(
            success is RegisterOutcome.Success,
            "第二步期望 Success，实际：$success",
        )

        // 第三次：新失败应反映本次真实异常，而非回填上一次的 IOException / "connection refused"。
        val nextFailed = client.register(identity()) as? RegisterOutcome.Failed
            ?: error("第三步期望 Failed")
        assertTrue(
            "RuntimeException" in nextFailed.reason,
            "新失败 reason 应含本次异常类名 RuntimeException，实际：${nextFailed.reason}",
        )
        assertTrue(
            "boom" in nextFailed.reason,
            "新失败 reason 应含本次异常消息 boom，实际：${nextFailed.reason}",
        )
        // 反向断言：确保上一次失败信息不再出现。
        assertEquals(
            false,
            "IOException" in nextFailed.reason,
            "新失败 reason 不应回填上一次 IOException：${nextFailed.reason}",
        )
        assertEquals(
            false,
            "connection refused" in nextFailed.reason,
            "新失败 reason 不应回填上一次连接失败原始消息：${nextFailed.reason}",
        )
    }
}
