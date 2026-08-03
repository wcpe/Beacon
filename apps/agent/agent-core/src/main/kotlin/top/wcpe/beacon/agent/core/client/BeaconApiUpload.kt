package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.browse.AssetContent
import top.wcpe.beacon.agent.core.command.IngestFile
import top.wcpe.beacon.agent.core.command.ScanFile
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.log.LogLine
import top.wcpe.beacon.agent.core.transport.HttpRequest

/**
 * 回传反向抓取结果：POST /beacon/v1/agent/files/ingest（FR-39，见 ADR-0027）。同步调用，请在异步线程使用。
 *
 * 携带命令 id + 文件集（path→content 文本）。控制面入库前同口径再校验（双保险）后复用 FileService.Import 落覆盖。
 * 200 视作成功；其它（命令态不符 / 校验失败 / 连接失败）返回 false（命令在控制面侧标 failed，agent 侧不重传）。
 */
fun BeaconApiClient.uploadIngest(
    commandId: Long,
    files: List<IngestFile>,
    identity: AgentIdentity? = null,
): Boolean {
    val body =
        mapOf(
            "commandId" to commandId,
            "files" to files.map { mapOf("path" to it.path, "content" to it.content) },
        )
    val resp =
        exec(
            HttpRequest(
                method = "POST",
                url = "$base/beacon/v1/agent/files/ingest",
                headers = headers(withBody = true, identity = identity),
                body = codec.encode(body),
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return false
    return resp.statusCode == 200
}

/**
 * 回传反向抓取 scan 清单：POST /beacon/v1/agent/files/scan（FR-58，见 ADR-0037）。同步调用，请在异步线程使用。
 *
 * 携带命令 id + 元信息清单（path/size/isText/overThreshold，**无 content**）。控制面存入任务 manifest、计数、转 pending-review。
 * 200 视作成功；其它（命令态不符 / 连接失败）返回 false（命令在控制面侧标 failed，agent 侧不重传）。
 */
fun BeaconApiClient.uploadScan(
    commandId: Long,
    files: List<ScanFile>,
    identity: AgentIdentity? = null,
): Boolean {
    val body =
        mapOf(
            "commandId" to commandId,
            "files" to
                files.map {
                    mapOf(
                        "path" to it.path,
                        "size" to it.size,
                        "isText" to it.isText,
                        "overThreshold" to it.overThreshold,
                    )
                },
        )
    val resp =
        exec(
            HttpRequest(
                method = "POST",
                url = "$base/beacon/v1/agent/files/scan",
                headers = headers(withBody = true, identity = identity),
                body = codec.encode(body),
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return false
    return resp.statusCode == 200
}

/**
 * 回传反向抓取执行错误：POST /beacon/v1/agent/files/error（FR-87）。同步调用，请在异步线程使用。
 *
 * agent 执行 scan/submit 读盘失败（IO 错 / 异常）时调用，把错误明细带给控制面，使任务转 failed 并记 lastError，
 * 不再让任务静默卡在非终态等过期清理。携命令 id + 原因文本。
 * 200 视作成功；其它（命令态不符 / 连接失败）返回 false（best-effort、不重试——回传不通仍交控制面超时清理为 expired）。
 */
fun BeaconApiClient.uploadError(
    commandId: Long,
    reason: String,
    identity: AgentIdentity? = null,
): Boolean {
    val body =
        mapOf(
            "commandId" to commandId,
            "reason" to reason,
        )
    val resp =
        exec(
            HttpRequest(
                method = "POST",
                url = "$base/beacon/v1/agent/files/error",
                headers = headers(withBody = true, identity = identity),
                body = codec.encode(body),
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return false
    return resp.statusCode == 200
}

/**
 * 回传 agent 自身日志快照：POST /beacon/v1/agent/logs（FR-88，见 ADR-0040）。同步调用，请在异步线程使用。
 *
 * 携带命令 id + 日志行集（级别 + 已脱敏文本）。日志在 agent 侧落环形缓冲那一刻即脱敏，本方法只忠实回传缓冲快照。
 * 控制面把日志行存为命令瞬态（取完即弃、不入真源、不进审计 detail）后 CAS done。
 * 200 视作成功；其它（命令态不符 / 连接失败）返回 false（命令在控制面侧标 failed / 超时清理，agent 侧不重传）。
 */
fun BeaconApiClient.uploadLogs(
    commandId: Long,
    lines: List<LogLine>,
    identity: AgentIdentity? = null,
): Boolean {
    val body =
        mapOf(
            "commandId" to commandId,
            "lines" to lines.map { mapOf("level" to it.level, "text" to it.text) },
        )
    val resp =
        exec(
            HttpRequest(
                method = "POST",
                url = "$base/beacon/v1/agent/logs",
                headers = headers(withBody = true, identity = identity),
                body = codec.encode(body),
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return false
    return resp.statusCode == 200
}

/**
 * 回传命令执行结果：POST /beacon/v1/agent/commands/result（FR-91）。同步调用，请在异步线程使用。
 *
 * 用于强制重同步（resync-config）这类无内容回传、仅需推进命令生命周期的命令：携命令 id + ok（成功）+ 失败原因。
 * 控制面据此 CAS 命令 done / failed。200 视作成功；其它（命令态不符 / 连接失败）返回 false（控制面侧超时清理兜底，agent 不重传）。
 */
fun BeaconApiClient.uploadCommandResult(
    commandId: Long,
    ok: Boolean,
    reason: String,
    identity: AgentIdentity? = null,
): Boolean {
    val body =
        mapOf(
            "commandId" to commandId,
            "ok" to ok,
            "reason" to reason,
        )
    val resp =
        exec(
            HttpRequest(
                method = "POST",
                url = "$base/beacon/v1/agent/commands/result",
                headers = headers(withBody = true, identity = identity),
                body = codec.encode(body),
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return false
    return resp.statusCode == 200
}

/**
 * 回传文件浏览结果：POST /beacon/v1/agent/files/browse-result（FR-110，见 ADR-0049）。同步调用，请在异步线程使用。
 *
 * 携命令 id + 身份（namespace/serverId）+ ok + 结果对象（[result]：列目录 / 子树 / 文件内容的结构化 Map）或失败原因。
 * ok=true 时 [result] 非空、[reason] 空；ok=false（原语返回 null：越权 / 非目录 / 非文本）时 [result] 空、[reason] 携原因。
 * 控制面据此把结果转存命令瞬态 + CAS done / failed，并唤醒等待中的 admin。
 * 200 视作成功；其它（命令态不符 / 连接失败）返回 false（best-effort、不重试——控制面超时清理兜底）。
 */
fun BeaconApiClient.uploadBrowseResult(
    identity: AgentIdentity,
    commandId: Long,
    ok: Boolean,
    result: Map<String, Any?>?,
    reason: String,
): Boolean {
    val body =
        buildMap {
            put("namespace", identity.namespace)
            put("serverId", identity.serverId)
            put("commandId", commandId)
            put("ok", ok)
            if (result != null) put("result", result)
            if (reason.isNotEmpty()) put("reason", reason)
        }
    val resp =
        exec(
            HttpRequest(
                method = "POST",
                url = "$base/beacon/v1/agent/files/browse-result",
                headers = headers(withBody = true, identity = identity),
                body = codec.encode(body),
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return false
    return resp.statusCode == 200
}

/**
 * 回传文件资产内容：POST /beacon/v2/agent/assets/content（FR-164，见 v2-file-assets.md §5.1）。同步调用，请在异步线程使用。
 *
 * 携命令 id + 身份（namespace/serverId）+ 内容元数据（binary/truncated/content）或读失败原因（error）。
 * [asset] 非空=读成功（含二进制回元数据）；null=读失败（越权 / 不存在 / 读盘异常），回**已脱敏**的通用原因。
 * 控制面据命令 id 定位命令（不依赖回传 path），内容为受审计的瞬态数据、转存内存中继唤醒等待的 admin，**绝不落库**。
 * 200 视作成功；其它（命令态不符 / 连接失败）返回 false（best-effort、不重试——控制面超时清理兜底）。
 */
fun BeaconApiClient.postAssetContent(
    identity: AgentIdentity,
    commandId: Long,
    asset: AssetContent?,
): Boolean {
    val body =
        buildMap<String, Any?> {
            put("commandId", commandId)
            put("namespace", identity.namespace)
            put("serverId", identity.serverId)
            if (asset != null) {
                put("binary", asset.binary)
                put("truncated", asset.truncated)
                put("content", asset.content)
            } else {
                put("error", "目标不存在或不可读") // 已脱敏、无凭据；真实原因仅落 agent 本地日志
            }
        }
    val resp =
        exec(
            HttpRequest(
                method = "POST",
                url = "$base/beacon/v2/agent/assets/content",
                headers = headers(withBody = true, identity = identity),
                body = codec.encode(body),
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return false
    return resp.statusCode == 200
}
