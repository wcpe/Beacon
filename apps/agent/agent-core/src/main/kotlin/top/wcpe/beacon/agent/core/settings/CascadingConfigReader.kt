package top.wcpe.beacon.agent.core.settings

/**
 * 三层优先级的配置读取器：**下发缓存 > config.yml > 代码默认**。
 *
 * ## 为什么用「读取器级联」而不是逐个字段判断
 *
 * `AgentBootstrap.readSettings` 里有 20+ 次读取。若在每个读取点手写
 * `pushed[key] ?: local[key] ?: default`，等于把同一条优先级规则复制 20 遍——
 * 改优先级要改 20 处，且新增一个配置项时极易漏掉某一层。
 *
 * 用读取器级联后，优先级**只有一处定义**：本类。上层代码继续按单层写法读取，
 * 完全不需要知道有几种配置来源。
 *
 * ## 类型转换的坑
 *
 * 下发缓存里一切都是字符串（为了能存成 JSON），而本地 config.yml 有原生类型。
 * 因此本类对 `long`/`double`/`boolean` 做「先看下发有没有、再把字符串转成目标类型」，
 * 转换失败（下发值非法）时**回退到下一层并记录**——而不是抛异常让 agent 起不来。
 * 这一点很重要：一个手误填错的下发值不该把整台服的 agent 变成砖。
 *
 * @param pushed 下发缓存（最高优先级）
 * @param local 本地 config.yml（离线兜底）
 * @param onInvalidPushed 下发值非法时的回调（供上层告警），参数为「键 + 原始值 + 目标类型」
 */
class CascadingConfigReader(
    private val pushed: Map<String, String>,
    private val local: ConfigReader,
    private val onInvalidPushed: (key: String, raw: String, targetType: String) -> Unit = { _, _, _ -> },
) : ConfigReader by local {
    /** 命中下发缓存的值（原始字符串）。 */
    private fun pushedValue(path: String): String? = pushed[path]?.takeIf { it.isNotBlank() }

    override fun int(
        path: String,
        default: Int,
    ): Int {
        val raw = pushedValue(path) ?: return local.int(path, default)
        val parsed = raw.trim().toIntOrNull()
        if (parsed == null) {
            onInvalidPushed(path, raw, "int")
            return local.int(path, default)
        }
        return parsed
    }

    override fun string(
        path: String,
        default: String,
    ): String = pushedValue(path) ?: local.string(path, default)

    override fun keys(path: String): Set<String> = local.keys(path)

    override fun stringList(path: String): List<String> = pushedValue(path)?.let { splitList(it) } ?: local.stringList(path)

    override fun long(
        path: String,
        default: Long,
    ): Long {
        val raw = pushedValue(path) ?: return local.long(path, default)
        val parsed = raw.trim().toLongOrNull()
        if (parsed == null) {
            onInvalidPushed(path, raw, "long")
            return local.long(path, default)
        }
        return parsed
    }

    override fun double(
        path: String,
        default: Double,
    ): Double {
        val raw = pushedValue(path) ?: return local.double(path, default)
        val parsed = raw.trim().toDoubleOrNull()
        if (parsed == null) {
            onInvalidPushed(path, raw, "double")
            return local.double(path, default)
        }
        return parsed
    }

    override fun boolean(
        path: String,
        default: Boolean,
    ): Boolean {
        val raw = pushedValue(path) ?: return local.boolean(path, default)
        return when (raw.trim().lowercase()) {
            "true" -> true
            "false" -> false
            else -> {
                onInvalidPushed(path, raw, "boolean")
                local.boolean(path, default)
            }
        }
    }

    /**
     * 把下发缓存里的列表值切成列表。
     *
     * 约定用逗号或换行分隔（JSON 里存数组更麻烦，而列表项本身极少含逗号——
     * 白名单是命令首 token、地址是 URL，都不含逗号）。去空白、丢空项。
     */
    private fun splitList(raw: String): List<String> =
        raw.split(',', '\n')
            .map { it.trim() }
            .filter { it.isNotEmpty() }
}
