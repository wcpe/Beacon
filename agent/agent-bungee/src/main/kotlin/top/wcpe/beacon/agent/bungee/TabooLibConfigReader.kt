package top.wcpe.beacon.agent.bungee

import top.wcpe.beacon.agent.core.settings.ConfigReader
import taboolib.module.configuration.Configuration

/**
 * 基于 TabooLib Configuration 的 ConfigReader 适配（使 core 不依赖 TabooLib）。
 *
 * 点分路径直接交给 Configuration 的层级取值。
 */
class TabooLibConfigReader(
    private val config: Configuration,
) : ConfigReader {

    override fun string(path: String, default: String): String = config.getString(path, default) ?: default

    override fun int(path: String, default: Int): Int = config.getInt(path, default)

    override fun long(path: String, default: Long): Long = config.getLong(path, default)

    override fun double(path: String, default: Double): Double = config.getDouble(path, default)

    override fun boolean(path: String, default: Boolean): Boolean = config.getBoolean(path, default)

    override fun stringList(path: String): List<String> = config.getStringList(path)

    override fun keys(path: String): Set<String> {
        val section = config.getConfigurationSection(path) ?: return emptySet()
        return section.getKeys(false)
    }
}
