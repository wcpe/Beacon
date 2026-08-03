package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.command.AgentCommand
import top.wcpe.beacon.agent.core.command.DeliveryCommandPayload
import top.wcpe.beacon.agent.core.command.IngestCommandPayload
import top.wcpe.beacon.agent.core.filetree.FileContent
import top.wcpe.beacon.agent.core.filetree.FileManifest
import top.wcpe.beacon.agent.core.filetree.FileManifestEntry
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.override.OverrideManifest
import top.wcpe.beacon.agent.core.override.OverrideSetEntry
import top.wcpe.beacon.agent.core.transport.HttpRequest

/**
 * 长轮询文件清单：GET /beacon/v1/agent/files/manifest（通道B）。
 *
 * 带当前 fileTreeMd5；变了 200 返回新清单（path→md5，不含内容），未变到超时 304。
 * 与配置长轮询唤醒集合独立（见 ADR-0010）。
 */
fun BeaconApiClient.pollFileManifest(
    identity: AgentIdentity,
    currentMd5: String?,
    timeoutMs: Long,
): FileManifestPollResult {
    val md5Param = currentMd5 ?: ""
    val url =
        buildString {
            append(base)
            append("/beacon/v1/agent/files/manifest")
            append("?namespace=").append(urlEncode(identity.namespace))
            append("&serverId=").append(urlEncode(identity.serverId))
            append("&md5=").append(urlEncode(md5Param))
            append("&timeoutMs=").append(timeoutMs)
        }
    // 读超时给长轮询留余量（挂起上限 + 普通读超时）。
    val resp =
        exec(
            HttpRequest(
                method = "GET",
                url = url,
                headers = headers(withBody = false, identity = identity),
                body = null,
                readTimeoutMs = timeoutMs + settings.requestTimeoutMs,
            ),
        ) ?: return FileManifestPollResult.Failed(connectFailReason())

    return when (resp.statusCode) {
        200 -> FileManifestPollResult.Changed(parseManifest(resp.body))
        304 -> FileManifestPollResult.NotModified
        404 -> FileManifestPollResult.NotRegistered
        else -> FileManifestPollResult.Failed("非预期状态码 ${resp.statusCode}")
    }
}

/**
 * 取单个文件内容：GET /beacon/v1/agent/files/content（通道B）。同步调用，请在异步线程使用。
 *
 * 200 返回该 path 按覆盖链解析后的整文件内容；404（FILE_NOT_FOUND/未注册）或连接失败返回 null。
 */
fun BeaconApiClient.fetchFileContent(
    identity: AgentIdentity,
    path: String,
): FileContent? {
    val url =
        buildString {
            append(base)
            append("/beacon/v1/agent/files/content")
            append("?namespace=").append(urlEncode(identity.namespace))
            append("&serverId=").append(urlEncode(identity.serverId))
            append("&path=").append(urlEncode(path))
        }
    val resp =
        exec(
            HttpRequest(
                method = "GET",
                url = url,
                headers = headers(withBody = false, identity = identity),
                body = null,
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        )
    if (resp == null || resp.statusCode != 200) {
        return null
    }
    val obj = JsonTree.asObject(codec.decode(resp.body))
    return FileContent(
        path = JsonTree.strOr(obj, "path", ""),
        md5 = JsonTree.strOr(obj, "md5", ""),
        content = JsonTree.strOr(obj, "content", ""),
    )
}

/**
 * 长轮询三方覆盖集投递：GET /beacon/v1/agent/override-sets（FR-15）。
 *
 * 带当前 overrideMd5；变了 200 返回适用覆盖集（目标根 + 命令 + 成员 path，不含内容），未变到超时 304。
 * 与文件长轮询复用同一唤醒集合（同属通道B），但 md5 维度独立（见 ADR-0011）。
 */
fun BeaconApiClient.pollOverrideSets(
    identity: AgentIdentity,
    currentMd5: String?,
    timeoutMs: Long,
): OverridePollResult {
    val md5Param = currentMd5 ?: ""
    val url =
        buildString {
            append(base)
            append("/beacon/v1/agent/override-sets")
            append("?namespace=").append(urlEncode(identity.namespace))
            append("&serverId=").append(urlEncode(identity.serverId))
            append("&md5=").append(urlEncode(md5Param))
            append("&timeoutMs=").append(timeoutMs)
        }
    val resp =
        exec(
            HttpRequest(
                method = "GET",
                url = url,
                headers = headers(withBody = false, identity = identity),
                body = null,
                readTimeoutMs = timeoutMs + settings.requestTimeoutMs,
            ),
        ) ?: return OverridePollResult.Failed(connectFailReason())

    return when (resp.statusCode) {
        200 -> OverridePollResult.Changed(parseOverrideManifest(resp.body))
        304 -> OverridePollResult.NotModified
        404 -> OverridePollResult.NotRegistered
        else -> OverridePollResult.Failed("非预期状态码 ${resp.statusCode}")
    }
}

/**
 * 取某覆盖集成员文件内容：GET /beacon/v1/agent/override-sets/content（FR-15）。同步调用，请在异步线程使用。
 *
 * 200 返回该 (set, path) 按覆盖链解析后的整文件内容；404 或连接失败返回 null（触发 fail-static 放弃本轮）。
 */
fun BeaconApiClient.fetchOverrideMember(
    identity: AgentIdentity,
    setName: String,
    path: String,
): FileContent? {
    val url =
        buildString {
            append(base)
            append("/beacon/v1/agent/override-sets/content")
            append("?namespace=").append(urlEncode(identity.namespace))
            append("&serverId=").append(urlEncode(identity.serverId))
            append("&set=").append(urlEncode(setName))
            append("&path=").append(urlEncode(path))
        }
    val resp =
        exec(
            HttpRequest(
                method = "GET",
                url = url,
                headers = headers(withBody = false, identity = identity),
                body = null,
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        )
    if (resp == null || resp.statusCode != 200) {
        return null
    }
    val obj = JsonTree.asObject(codec.decode(resp.body))
    return FileContent(
        path = JsonTree.strOr(obj, "path", ""),
        md5 = JsonTree.strOr(obj, "md5", ""),
        content = JsonTree.strOr(obj, "content", ""),
    )
}

/**
 * 拉取本 agent 一条待办命令：GET /beacon/v1/agent/commands（FR-39，见 ADR-0027）。同步调用，请在异步线程使用。
 *
 * 收到 SSE command-pending 事件 / SSE READY 后调用。
 * 200 返回命令（控制面已 CAS 迁移 fetched）；204 无待办返回 null；其它状态（404 未注册 / 连接失败）一律返回 null
 * （命令流为 best-effort：拉不到本轮静默放弃，下次事件 / 重连再拉，不影响配置主流程）。
 */
fun BeaconApiClient.fetchPendingCommand(identity: AgentIdentity): AgentCommand? {
    val url =
        buildString {
            append(base)
            append("/beacon/v1/agent/commands")
            append("?namespace=").append(urlEncode(identity.namespace))
            append("&serverId=").append(urlEncode(identity.serverId))
        }
    val resp =
        exec(
            HttpRequest(
                method = "GET",
                url = url,
                headers = headers(withBody = false, identity = identity),
                body = null,
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return null
    return when (resp.statusCode) {
        200 -> parsePendingCommand(resp.body)
        204 -> null // 无待办命令
        else -> null // 404 未注册 / 其它：本轮放弃（best-effort）
    }
}

internal fun BeaconApiClient.parseManifest(jsonBody: String): FileManifest {
    val obj = JsonTree.asObject(codec.decode(jsonBody))
    val entries =
        JsonTree.asList(obj["files"]).map { raw ->
            val fileObj = JsonTree.asObject(raw)
            FileManifestEntry(
                path = JsonTree.strOr(fileObj, "path", ""),
                md5 = JsonTree.strOr(fileObj, "md5", ""),
            )
        }
    return FileManifest(
        namespace = JsonTree.strOr(obj, "namespace", ""),
        serverId = JsonTree.strOr(obj, "serverId", ""),
        group = JsonTree.str(obj, "group"),
        zone = JsonTree.str(obj, "zone"),
        fileTreeMd5 = JsonTree.strOr(obj, "fileTreeMd5", ""),
        entries = entries,
    )
}

internal fun BeaconApiClient.parseOverrideManifest(jsonBody: String): OverrideManifest {
    val obj = JsonTree.asObject(codec.decode(jsonBody))
    val sets =
        JsonTree.asList(obj["sets"]).map { raw ->
            val setObj = JsonTree.asObject(raw)
            OverrideSetEntry(
                name = JsonTree.strOr(setObj, "name", ""),
                targetRoot = JsonTree.strOr(setObj, "targetRoot", ""),
                // 空命令在控制面投递为 ""；归一化为 null（不下发命令），与 OverrideApplier 入参语义一致。
                reloadCommand = JsonTree.strOr(setObj, "reloadCommand", "").ifEmpty { null },
                members = JsonTree.asList(setObj["members"]).map { m -> JsonTree.asString(m) },
            )
        }
    return OverrideManifest(
        namespace = JsonTree.strOr(obj, "namespace", ""),
        serverId = JsonTree.strOr(obj, "serverId", ""),
        overrideMd5 = JsonTree.strOr(obj, "overrideMd5", ""),
        sets = sets,
    )
}

/**
 * 解析待办命令响应（FR-39 / FR-58）：
 * `{"id":<n>,"type":"ingest-plugins","payload":{"scope","group","target","mode","selectedPaths":[...]}}`。
 *
 * payload 缺失字段按空兜底；`mode` 区分 scan / submit（缺失=旧整树行为，向后兼容），`selectedPaths` 仅 submit 用。
 * agent 据 mode/selectedPaths 决定两段式分路（见 ReverseFetchExecutor）；scope/group/target 仍仅供日志可读 + 回传 id。
 */
internal fun BeaconApiClient.parsePendingCommand(jsonBody: String): AgentCommand {
    val obj = JsonTree.asObject(codec.decode(jsonBody))
    val payloadObj = JsonTree.asObject(obj["payload"])
    val type = JsonTree.strOr(obj, "type", "")
    // 交付命令（FR-165）：payload 只含控制信息，解析到独立的 DeliveryCommandPayload（不膨胀 IngestCommandPayload）。
    // activationMethod 仅 delivery_activate 携带（restart / hot_reload，FR-171 §4.6.1）；其它 delivery 命令缺省空串。
    val deliveryPayload =
        if (AgentCommand.isDeliveryType(type)) {
            DeliveryCommandPayload(
                orderId = JsonTree.longOr(payloadObj, "orderId", 0L),
                activationMethod = JsonTree.strOr(payloadObj, "activationMethod", ""),
            )
        } else {
            null
        }
    return AgentCommand(
        id = JsonTree.longOr(obj, "id", 0L),
        type = type,
        deliveryPayload = deliveryPayload,
        payload =
            IngestCommandPayload(
                scope = JsonTree.strOr(payloadObj, "scope", ""),
                group = JsonTree.strOr(payloadObj, "group", ""),
                target = JsonTree.strOr(payloadObj, "target", ""),
                mode = JsonTree.strOr(payloadObj, "mode", ""),
                selectedPaths = JsonTree.asList(payloadObj["selectedPaths"]).map { JsonTree.asString(it) },
                // 文件浏览字段（FR-110，仅 fs-browse 命令携带；其它命令缺省为空，向后兼容）。
                op = JsonTree.strOr(payloadObj, "op", ""),
                path = JsonTree.strOr(payloadObj, "path", ""),
                offset = JsonTree.intOr(payloadObj, "offset", 0),
                limit = JsonTree.intOr(payloadObj, "limit", 0),
                maxDepth = JsonTree.intOr(payloadObj, "maxDepth", 0),
                // 文件资产重扫字段（FR-163，仅 asset-rescan 命令携带；其它命令缺省 false，向后兼容）。
                force = JsonTree.boolOr(payloadObj, "force", false),
                // 文件资产读取上限（FR-164，仅 asset-read 命令携带；其它命令缺省 0，向后兼容）。
                maxBytes = JsonTree.intOr(payloadObj, "maxBytes", 0),
            ),
    )
}
