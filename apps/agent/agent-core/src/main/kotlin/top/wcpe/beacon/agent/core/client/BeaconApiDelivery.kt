package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.delivery.DeliveryManifestFile
import top.wcpe.beacon.agent.core.delivery.DeliveryStageReport
import top.wcpe.beacon.agent.core.delivery.DeliveryTargetManifest
import top.wcpe.beacon.agent.core.delivery.DeliveryUploadItem
import top.wcpe.beacon.agent.core.delivery.DeliveryUploadManifest
import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.transport.HttpRequest

/**
 * 拉取模板源待上传 blob 清单：GET /beacon/v2/agent/delivery/orders/{id}/upload-manifest（FR-165，spec §5.2）。
 * 同步调用，请在异步线程使用。
 *
 * 200 返回待上传项（path/sha256/size，已就绪 blob 不在列）；其它（403 非模板源 / 404 单不存在 / 连接失败）返回 null
 * （交付上传流程据此回执 failed）。
 */
fun BeaconApiClient.fetchDeliveryUploadManifest(
    identity: AgentIdentity,
    orderId: Long,
): DeliveryUploadManifest? {
    val resp =
        exec(
            HttpRequest(
                method = "GET",
                url = "$base/beacon/v2/agent/delivery/orders/$orderId/upload-manifest",
                headers = headers(withBody = false, identity = identity),
                body = null,
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return null
    if (resp.statusCode != 200) return null
    return parseDeliveryUploadManifest(resp.body)
}

/**
 * 拉取目标本服差异清单：GET /beacon/v2/agent/delivery/orders/{id}/manifest（FR-165，spec §5.2）。
 * 同步调用，请在异步线程使用。
 *
 * 200 返回文件项（sourceKind/path/action/sha256/size）与生效方式；普通文件差异和配置冻结工件统一在 files 中。
 * 其它（403 非目标 / 404 / 连接失败）返回 null（推送或生效流程据此回执 failed）。
 */
fun BeaconApiClient.fetchDeliveryManifest(
    identity: AgentIdentity,
    orderId: Long,
): DeliveryTargetManifest? {
    val resp =
        exec(
            HttpRequest(
                method = "GET",
                url = "$base/beacon/v2/agent/delivery/orders/$orderId/manifest",
                headers = headers(withBody = false, identity = identity),
                body = null,
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return null
    if (resp.statusCode != 200) return null
    return parseDeliveryManifest(resp.body)
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
    return resp.statusCode == 204
}

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
