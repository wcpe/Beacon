package top.wcpe.beacon.agent.core.config

/**
 * 单个有效配置项（已按覆盖链合并后的文本）。
 *
 * @param dataId  配置名（如 mysql.yml）
 * @param format  格式（yaml / properties / json）
 * @param md5     单项 md5（小写 hex）
 * @param content 合并后原始文本
 */
data class ConfigItem(
    val dataId: String,
    val format: String,
    val md5: String,
    val content: String,
)
