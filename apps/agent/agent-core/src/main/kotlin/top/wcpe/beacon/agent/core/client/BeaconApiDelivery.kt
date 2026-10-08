package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.delivery.DeliveryManifestFile
import top.wcpe.beacon.agent.core.delivery.DeliveryStageReport
import top.wcpe.beacon.agent.core.delivery.DeliveryTargetManifest
import top.wcpe.beacon.agent.core.delivery.DeliveryUploadItem
import top.wcpe.beacon.agent.core.delivery.DeliveryUploadManifest
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.log.LogRedactor
import top.wcpe.beacon.agent.core.transport.HttpRequest

/**
 * 拉取模板源待上传 blob 清单：GET /beacon/v2/agent/delivery/orders/{id}/upload-manifest（FR-165，spec §5.2）。
 * 同步调用，请在异步线程使用。
 *
 * 200 返回待上传项（path/sha256/size，已就绪 blob 不在列）；其它（403 非模板源 / 404 单不存在 / 连接失败）
 * 返回失败结果并**保留状态码与错误码**（交付上传流程据此回执 failed，原因可诊断）。
 */
fun BeaconApiClient.fetchDeliveryUploadManifest(
    identity: AgentIdentity,
    orderId: Long,
): DeliveryFetchResult<DeliveryUploadManifest> {
    val resp =
        exec(
            HttpRequest(
                method = "GET",
                url = "$base/beacon/v2/agent/delivery/orders/$orderId/upload-manifest",
                headers = headers(withBody = false, identity = identity),
                body = null,
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return deliveryNotSent()
    if (resp.statusCode != HTTP_OK) return deliveryFailure(resp.statusCode, resp.body)
    return parseOrFail(resp) { parseDeliveryUploadManifest(it) }
}

/**
 * 拉取目标本服差异清单：GET /beacon/v2/agent/delivery/orders/{id}/manifest（FR-165，spec §5.2）。
 * 同步调用，请在异步线程使用。
 *
 * 200 返回文件项（sourceKind/path/action/sha256/size）与生效方式；普通文件差异和配置冻结工件统一在 files 中。
 * 其它（403 非目标 / 404 / 连接失败）返回失败结果并保留状态码与错误码（推送或生效流程据此回执 failed）。
 */
fun BeaconApiClient.fetchDeliveryManifest(
    identity: AgentIdentity,
    orderId: Long,
): DeliveryFetchResult<DeliveryTargetManifest> {
    val resp =
        exec(
            HttpRequest(
                method = "GET",
                url = "$base/beacon/v2/agent/delivery/orders/$orderId/manifest",
                headers = headers(withBody = false, identity = identity),
                body = null,
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return deliveryNotSent()
    if (resp.statusCode != HTTP_OK) return deliveryFailure(resp.statusCode, resp.body)
    return parseOrFail(resp) { parseDeliveryManifest(it) }
}

/**
 * 回执交付阶段结果：POST /beacon/v2/agent/delivery/orders/{id}/result（FR-165，spec §5.2）。
 * 同步调用，请在异步线程使用。
 *
 * 报文字段（phase / status / 计数 / backupPresent / 脱敏 error）打包在 [report] 中。
 * 204 视作成功；其它（命令态不符 / 连接失败）返回 false（best-effort，控制面按命令超时清理兜底）。
 */
fun BeaconApiClient.postDeliveryResult(
    identity: AgentIdentity,
    orderId: Long,
    report: DeliveryStageReport,
): Boolean {
    val body =
        buildMap<String, Any?> {
            put("phase", report.phase)
            put("status", report.status)
            put("changedFileCount", report.changedFileCount)
            put("skippedFileCount", report.skippedFileCount)
            put("backupPresent", report.backupPresent)
            put("error", report.error)
        }
    val resp =
        exec(
            HttpRequest(
                method = "POST",
                url = "$base/beacon/v2/agent/delivery/orders/$orderId/result",
                headers = headers(withBody = true, identity = identity),
                body = codec.encode(body),
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return false
    return resp.statusCode == HTTP_NO_CONTENT
}

/** 200 响应体解析；解析异常（非 JSON / 结构不符）→ 失败结果而非上抛，避免打断命令排空循环。 */
private fun <T> BeaconApiClient.parseOrFail(
    resp: top.wcpe.beacon.agent.core.transport.HttpResponse,
    parse: (String) -> T,
): DeliveryFetchResult<T> =
    try {
        DeliveryFetchResult(parse(resp.body), resp.statusCode, "")
    } catch (e: Exception) {
        DeliveryFetchResult(null, resp.statusCode, "HTTP ${resp.statusCode} ${e.javaClass.simpleName}: 响应体解析失败")
    }

/** 连接级失败（请求未发出 / 无响应）结果：statusCode=[HTTP_NOT_SENT]，说明控制面不可达并附最近一次连接失败原因。 */
private fun BeaconApiClient.deliveryNotSent(): DeliveryFetchResult<Nothing> =
    DeliveryFetchResult(null, HTTP_NOT_SENT, "请求未发出（控制面不可达 / 连接失败）：${connectFailReason()}")

/** 非 200 失败结果：HTTP 码 + 服务端错误码 / 说明（脱敏并截断），无明细时只给码。 */
private fun BeaconApiClient.deliveryFailure(
    statusCode: Int,
    body: String,
): DeliveryFetchResult<Nothing> {
    val detail = deliveryErrorDetail(body)
    return DeliveryFetchResult(null, statusCode, "HTTP $statusCode" + if (detail.isEmpty()) "" else " $detail")
}

/**
 * 从错误响应体取「错误码: 说明」（camelCase `code` / `message`，与控制面 render.WriteError 一致）。
 *
 * 取不到（空体 / 非 JSON / 字段缺失）返回空串——宁少给上下文也不抛异常（本函数只服务于失败摘要）。
 * 展示前先经 [LogRedactor] 脱敏（治法在源头，见 error-surfacing 规则），并按 [MAX_ERROR_DETAIL_CHARS] 截断。
 */
private fun BeaconApiClient.deliveryErrorDetail(body: String): String {
    val obj = decodeBodyOrEmpty(body)
    val parts = listOf(JsonTree.strOr(obj, "code", ""), JsonTree.strOr(obj, "message", "")).filter { it.isNotEmpty() }
    return if (parts.isEmpty()) "" else LogRedactor.redact(parts.joinToString(": ")).take(MAX_ERROR_DETAIL_CHARS)
}

/** 解响应体为对象树；空体 / 非 JSON 返回空表（失败摘要只求尽力，不因解析问题放大成异常）。 */
private fun BeaconApiClient.decodeBodyOrEmpty(body: String): Map<String, Any?> =
    if (body.isEmpty()) {
        emptyMap()
    } else {
        try {
            JsonTree.asObject(codec.decode(body))
        } catch (e: Exception) {
            emptyMap()
        }
    }

/** 失败摘要最大字符数：够定位原因，又不至于把整段控制面文案灌进回执与日志。 */
private const val MAX_ERROR_DETAIL_CHARS = 240

/** 成功码（清单拉取）。 */
private const val HTTP_OK = 200

/** 成功码（阶段回执）。 */
private const val HTTP_NO_CONTENT = 204

/** 解析待上传清单响应（orderId + items[path/sha256/size]，camelCase 键，缺失项按空 / 0 兜底）。 */
internal fun BeaconApiClient.parseDeliveryUploadManifest(jsonBody: String): DeliveryUploadManifest {
    val obj = JsonTree.asObject(codec.decode(jsonBody))
    val items =
        JsonTree.asList(obj["items"]).map { raw ->
            val itemObj = JsonTree.asObject(raw)
            DeliveryUploadItem(
                path = JsonTree.strOr(itemObj, "path", ""),
                sha256 = JsonTree.strOr(itemObj, "sha256", ""),
                sizeBytes = JsonTree.longOr(itemObj, "size", 0L),
            )
        }
    return DeliveryUploadManifest(orderId = JsonTree.longOr(obj, "orderId", 0L), items = items)
}

/** 解析目标差异清单响应；files.sourceKind 缺失时兼容为 file_diff。 */
internal fun BeaconApiClient.parseDeliveryManifest(jsonBody: String): DeliveryTargetManifest {
    val obj = JsonTree.asObject(codec.decode(jsonBody))
    val files =
        JsonTree.asList(obj["files"]).map { raw ->
            val fileObj = JsonTree.asObject(raw)
            DeliveryManifestFile(
                path = JsonTree.strOr(fileObj, "path", ""),
                action = JsonTree.strOr(fileObj, "action", ""),
                sha256 = JsonTree.strOr(fileObj, "sha256", ""),
                sizeBytes = JsonTree.longOr(fileObj, "size", 0L),
                sourceKind =
                    if (fileObj.containsKey("sourceKind")) {
                        JsonTree.str(fileObj, "sourceKind") ?: ""
                    } else {
                        DeliveryManifestFile.SOURCE_KIND_FILE_DIFF
                    },
            )
        }
    return DeliveryTargetManifest(
        orderId = JsonTree.longOr(obj, "orderId", 0L),
        activationMethod = JsonTree.strOr(obj, "activationMethod", ""),
        files = files,
    )
}
