package top.wcpe.beacon.agent.core.testutil

import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.BackoffSettings
import top.wcpe.beacon.agent.core.settings.FileTreeSettings
import top.wcpe.beacon.agent.core.settings.OverrideSettings
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse
import top.wcpe.beacon.agent.core.transport.HttpTransport
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicReference

/**
 * 声明端点（FR-243）单测用的假 HTTP 传输：按预置状态码 / 响应体应答，可模拟控制面不可达，
 * 并记录调用次数与最近一次请求供报文契约断言。
 */
class FakeHttpTransport(
    @Volatile var status: Int = 200,
    @Volatile var body: String = "",
) : HttpTransport {
    /** 置 true 模拟连接级失败：execute 抛异常，被 BeaconApiClient.exec 吞为 null → 调用方判 UNAVAILABLE。 */
    @Volatile
    var down: Boolean = false

    val calls = AtomicInteger(0)

    val lastRequest = AtomicReference<HttpRequest?>(null)

    override fun execute(request: HttpRequest): HttpResponse {
        calls.incrementAndGet()
        lastRequest.set(request)
        if (down) throw RuntimeException("模拟控制面不可达")
        return HttpResponse(status, body)
    }
}

/**
 * 记录 encode 入参、按预置泛型树应答 decode 的 codec（声明端点单测用）。
 *
 * @param decodedTree decode 一律返回的泛型树（各用例按响应体语义自行预置）
 */
class CapturingCodec(
    @Volatile var decodedTree: Any? = emptyMap<String, Any?>(),
) : JsonCodec {
    val lastEncoded = AtomicReference<Any?>(null)

    override fun encode(value: Any?): String {
        lastEncoded.set(value)
        return "encoded"
    }

    override fun decode(json: String): Any? = decodedTree
}

/** 测试用 agent 身份（带 v2 身份，供鉴权头注入）。 */
fun declarationIdentity(): AgentIdentity =
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

/** 测试用 agent 设置。 */
fun declarationSettings(): AgentSettings =
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
