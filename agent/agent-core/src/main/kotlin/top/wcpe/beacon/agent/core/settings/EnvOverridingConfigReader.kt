package top.wcpe.beacon.agent.core.settings

/**
 * ConfigReader 装饰器（FR-33）：让 config.yml 的标量与列表配置项都能被环境变量覆盖，env 优先于文件。
 *
 * 环境变量名约定：BEACON_AGENT_ + 配置点分路径转大写、'.' 与 '-' 均转 '_'。
 * 例：identity.server-id → BEACON_AGENT_IDENTITY_SERVER_ID。
 *
 * - 标量（string/int/long/double/boolean）：env 有非空值即解析覆盖；解析失败回落 delegate（不崩）。
 * - 列表（stringList）：env 值按 ',' 分隔、trim、去空项。
 * - 动态键 map（keys，如 identity.metadata）：本版不支持 env 覆盖，直接委托 delegate。
 *
 * env 查找以函数注入（如 System::getenv），core 不依赖具体环境读取、便于单测。
 * 空串视为「未设置」回落文件（本版不支持用 env 把非空文件值显式改成空）。
 */
class EnvOverridingConfigReader(
    private val delegate: ConfigReader,
    private val env: (String) -> String?,
) : ConfigReader {

    override fun string(path: String, default: String): String =
        envValue(path) ?: delegate.string(path, default)

    override fun int(path: String, default: Int): Int {
        val raw = envValue(path) ?: return delegate.int(path, default)
        return raw.toIntOrNull() ?: delegate.int(path, default)
    }

    override fun long(path: String, default: Long): Long {
        val raw = envValue(path) ?: return delegate.long(path, default)
        return raw.toLongOrNull() ?: delegate.long(path, default)
    }

    override fun double(path: String, default: Double): Double {
        val raw = envValue(path) ?: return delegate.double(path, default)
        return raw.toDoubleOrNull() ?: delegate.double(path, default)
    }

    override fun boolean(path: String, default: Boolean): Boolean {
        val raw = envValue(path) ?: return delegate.boolean(path, default)
        return when (raw.trim().lowercase()) {
            "true" -> true
            "false" -> false
            else -> delegate.boolean(path, default)
        }
    }

    override fun stringList(path: String): List<String> {
        val raw = envValue(path) ?: return delegate.stringList(path)
        return raw.split(",").map { it.trim() }.filter { it.isNotEmpty() }
    }

    /** 动态键 map（如 identity.metadata）本版不支持 env 覆盖，直接委托文件。 */
    override fun keys(path: String): Set<String> = delegate.keys(path)

    /** 取该配置路径对应环境变量的非空值；为 null 或空串视为「未设置」。 */
    private fun envValue(path: String): String? {
        val v = env(envName(path))
        return if (v.isNullOrEmpty()) null else v
    }

    private companion object {
        /** 点分路径 → 环境变量名：BEACON_AGENT_ + 大写、'.' 与 '-' 转 '_'。 */
        fun envName(path: String): String =
            "BEACON_AGENT_" + path.uppercase().replace('.', '_').replace('-', '_')
    }
}
