package top.wcpe.beacon.agent.core.stream

import top.wcpe.beacon.agent.core.client.JsonTree
import top.wcpe.beacon.agent.core.transport.JsonCodec

/**
 * server→agent SSE 推送的事件类型常量（与控制面 internal/sse 一一对应，见 ADR-0015）。
 *
 * 流只发"变更通知"：agent 收到 *-changed 后用现有 HTTP 端点取内容并应用（取数据-应用逻辑复用，不变）。
 */
object StreamEventTypes {

    /** 配置（通道A）有效配置变更：agent 据此强制重拉 config/effective。 */
    const val CONFIG_CHANGED = "config-changed"

    /** 文件树（通道B）有效清单变更：agent 据此强制重拉 files/manifest 并增量同步。 */
    const val FILE_CHANGED = "file-changed"

    /** 三方覆盖集（FR-15）适用集合变更：agent 据此强制重拉 override-sets 并落盘。 */
    const val OVERRIDE_CHANGED = "override-changed"

    /** 拓扑变更（FR-29）：namespace 内实例上线/下线/改派；agent 据此回调拓扑监听器，业务侧重查发现端点。 */
    const val TOPOLOGY_CHANGED = "topology-changed"

    /** 首轮对账完成标记：agent 收到即知"断线期间落下的增量已补发完、转入直播"。 */
    const val READY = "ready"
}

/**
 * *-changed 事件载荷解析：从 data 行 JSON（{"md5":"x"}）取新 md5。
 *
 * 经 JsonCodec 解码为泛型树（core 不碰 @Serializable，守 ADR-0005）；缺字段返回空串。
 */
object ChangedPayload {

    /** 从事件 data 文本解析新 md5；解析失败或缺字段返回空串（调用方按"强制重拉"兜底）。 */
    fun md5Of(codec: JsonCodec, data: String): String {
        if (data.isBlank()) return ""
        val tree = runCatching { codec.decode(data) }.getOrNull() ?: return ""
        val obj = JsonTree.asObject(tree)
        return JsonTree.strOr(obj, "md5", "")
    }
}
