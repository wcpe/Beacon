package top.wcpe.beacon.agent.core.override

/**
 * 三方覆盖集投递清单（对应 override-sets 200 响应，FR-15）。
 *
 * 只含"目标根 + 受限重载命令 + 成员 path"事实（不含成员内容）；agent 据 overrideMd5 与本地比对，
 * 变了再逐个取成员内容落 targetRoot。与配置 md5、fileTreeMd5 相互独立（见 ADR-0011）。
 *
 * @param namespace   环境
 * @param serverId    本机身份
 * @param overrideMd5 适用覆盖集整体指纹（小写 hex）
 * @param sets        适用覆盖集列表（按 name 字典序）
 */
data class OverrideManifest(
    val namespace: String,
    val serverId: String,
    val overrideMd5: String,
    val sets: List<OverrideSetEntry>,
)

/**
 * 清单中单个适用覆盖集（不含成员内容）。
 *
 * @param name          覆盖集名称（取成员内容时回传定位；控制面权威归属）
 * @param targetRoot    目标插件根目录（相对 plugins，agent 落盘根）
 * @param reloadCommand 受限重载命令（可空表示不下发；是否真正派发由 agent 本地白名单把关）
 * @param members       成员文件相对 path 清单（内容经 override-sets/content 取）
 */
data class OverrideSetEntry(
    val name: String,
    val targetRoot: String,
    val reloadCommand: String?,
    val members: List<String>,
)
