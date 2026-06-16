package top.wcpe.beacon.agent.core.identity

/**
 * agent 本机身份（注册请求体的身份与标签部分）。
 *
 * group/zone 不在此声明——zone 由控制面权威指派，group 由控制面解析回填（见注册响应）。
 *
 * @param namespace 环境（prod / test）
 * @param serverId  本机唯一身份，环境内唯一
 * @param role      角色（bukkit / bungee），壳层固定
 * @param groupHint 大区提示（仅未分配 zone 时作兜底 group）
 * @param address   对外可达地址 ip:port
 * @param version   本服业务版本字符串（发现过滤标签，可空）
 * @param capacity  容量（顶层一等字段）
 * @param weight    权重（顶层一等字段）
 * @param metadata  自定义元数据标签（仅 map<string,string>，无 canary）
 */
data class AgentIdentity(
    val namespace: String,
    val serverId: String,
    val role: String,
    val groupHint: String,
    val address: String,
    val version: String,
    val capacity: Int,
    val weight: Int,
    val metadata: Map<String, String>,
) {
    /** 身份是否合法（serverId / namespace 非空）。 */
    fun isValid(): Boolean = serverId.isNotBlank() && namespace.isNotBlank()
}
