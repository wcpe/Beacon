package top.wcpe.beacon.agent.core.settings

import top.wcpe.beacon.agent.core.identity.AgentCredentials
import top.wcpe.beacon.agent.core.identity.AgentCredentialsStore

/**
 * 统一解析 agent 的生效设置：三层优先级 + 凭据合并。
 *
 * ## 为什么独立成类
 *
 * 两个平台壳（Bukkit / Bungee）需要完全相同的解析逻辑，但它们的插件主类都已经贴着
 * detekt 的函数数上限，再各自放一份必然超限；复制两份又会让「优先级怎么定」有两个答案。
 * 抽到这里后，壳层只留一行调用，规则只有一处。
 *
 * 优先级（ADR-0095）：**Web 下发缓存 > 环境变量 > config.yml > 代码默认**。
 * 接入目标与身份（`beacon.endpoints` / `beacon.bootstrap-token` / `identity.*`）
 * **只能来自本地**，下发缓存里的这些键会被 [PushedConfigPolicy] 挡掉。
 *
 * @param localReader 本地配置（已含环境变量覆盖）
 * @param pushedStore 下发缓存
 * @param credentialsStore 身份凭据
 * @param warn 告警出口
 * @param info 信息出口
 */
class EffectiveSettingsResolver(
    private val localReader: ConfigReader,
    private val pushedStore: PushedConfigStore,
    private val credentialsStore: AgentCredentialsStore,
    private val warn: (String) -> Unit,
    private val info: (String) -> Unit,
) {
    /**
     * 解析出生效设置。
     *
     * 每次调用都重读三处来源：join 写过凭据、下发刚更新过缓存，
     * 复用旧值会让「刚 join 完却仍不接入」这类问题非常难查。
     */
    fun resolve(): AgentSettings {
        val pushed = pushedStore.load()
        // 键级防护：下发里的接入目标/身份一律丢弃（并告知，避免「Web 改了没生效」无从排查）。
        val (allowed, rejected) = PushedConfigPolicy.partition(pushed.values)
        if (rejected.isNotEmpty()) {
            warn("下发配置含禁止项已被忽略（接入目标与身份只能本地决定）：${rejected.joinToString(", ")}")
        }
        val reader =
            CascadingConfigReader(
                pushed = allowed,
                local = localReader,
                onInvalidPushed = { key, raw, type ->
                    warn("下发配置值非法已忽略：$key=$raw（期望 $type），回退到本地配置")
                },
            )
        return mergeCredentials(AgentBootstrap.readSettings(reader))
    }

    /**
     * 是否具备接入条件。
     *
     * **未接入不是配置错误**：零配置形态下运维还没 join 时，agent 该静静等待，
     * 不该刷 ERROR 让人以为装坏了。只有「配了但配错」才值得警告。
     */
    fun hasConnectionConfig(settings: AgentSettings): Boolean {
        if (settings.endpoints.isNotEmpty() && settings.bootstrapToken.isNotBlank()) {
            return true
        }
        val partial = settings.endpoints.isNotEmpty() || settings.bootstrapToken.isNotBlank()
        if (partial) {
            warn("接入配置不完整（endpoints/token 二者需同时具备），Beacon agent 不接入控制面")
        } else {
            info("尚未接入 Beacon。使用 /beacon join <控制面地址> <令牌> 完成接入，或在 config.yml 配置")
        }
        return false
    }

    /**
     * 把凭据里的地址/令牌合并进设置。
     *
     * `config.yml` 显式配了就以它为准：运维手写的意图优先，也是旧部署路径的兼容保证。
     */
    private fun mergeCredentials(settings: AgentSettings): AgentSettings {
        val credentials: AgentCredentials = credentialsStore.load() ?: return settings
        if (!credentials.canConnect) {
            return settings
        }
        if (settings.endpoints.isNotEmpty() && settings.bootstrapToken.isNotBlank()) {
            return settings
        }
        return settings.copy(endpoints = listOf(credentials.endpoint), bootstrapToken = credentials.token)
    }
}
