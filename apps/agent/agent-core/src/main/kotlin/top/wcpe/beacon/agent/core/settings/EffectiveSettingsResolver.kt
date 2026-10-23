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
        // 缓存读不出来（损坏 / 版本不支持 / 读失败）必须**显式告警**（FR-277 #6）：
        // 否则「这台机器没被下发过」和「缓存坏了」在运维眼里长得一模一样。
        val pushed = pushedStore.load(warn)
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
        // 本地配置的越界收口同样要告警（FR-280 #3）：运维手写的越界值不该被悄悄改掉。
        return mergeCredentials(AgentBootstrap.readSettings(reader, warn))
    }

    /**
     * 是否具备接入条件。
     *
     * **未接入不是配置错误**：零配置形态下运维还没 join 时，agent 该静静等待，
     * 不该刷 ERROR 让人以为装坏了。只有「配了但配错」才值得警告。
     */
    fun hasConnectionConfig(settings: AgentSettings): Boolean {
        if (hasRealConnectionConfig(settings)) {
            return true
        }
        // 配了东西但全是占位符：这是最容易让人以为「装好了连不上」的形态，必须说清楚。
        val placeholderOnly =
            (settings.endpoints.any { it.isNotBlank() } || settings.bootstrapToken.isNotBlank()) &&
                !hasRealConnectionConfig(settings)
        if (placeholderOnly) {
            warn(
                "config.yml 里的接入信息仍是模板占位符（如 <CONTROL_PLANE_URL>），已按「未配置」处理：" +
                    "请填入真实地址与令牌，或清空后使用 /beacon join；占位符不会写入凭据",
            )
            return false
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
     * config.yml 里是否**填了真实的接入信息**（占位符不算）。
     *
     * 出厂模板曾把 `<CONTROL_PLANE_URL>` / `<NAMESPACE_ACCESS_TOKEN>` 当作示例值，
     * 两者都非空 —— 于是被当成「运维已显式配置」：`/beacon join` 被静默忽略、
     * agent 对着一个不存在的地址无限重连，而且占位符还会被写进凭据文件，
     * 之后即使清空 config.yml 也再救不回来（凭据仍声称「我能连」）。
     *
     * 模板现在填空，但**已发出的 jar 与被复制过的旧模板仍在外面**，故此处保留识别：
     * 让占位符等价于「未配置」，退回凭据 / 静待 join，绝不把它当成真实目标。
     */
    private fun hasRealConnectionConfig(settings: AgentSettings): Boolean =
        settings.endpoints.any { it.isNotBlank() && !isPlaceholder(it) } &&
            settings.bootstrapToken.isNotBlank() && !isPlaceholder(settings.bootstrapToken)

    /**
     * 形如 `<CONTROL_PLANE_URL>` / `${...}` 的模板占位符。
     *
     * 判据刻意保守（整串被尖括号或 `${}` 包住才算），避免把真实地址里的字符误判。
     */
    private fun isPlaceholder(raw: String): Boolean = looksLikePlaceholder(raw)

    companion object {
        /**
         * 过滤掉模板占位符，返回可安全用作「凭据种子」的接入信息。
         *
         * 壳层在新建/迁移凭据时把 config.yml 的值当种子传下去。若不过滤，占位符会被
         * 写进 `agent-credentials.json` 且 `canConnect == true` —— 此后即使清空 config.yml，
         * agent 仍会对着占位符地址重连，而凭据文件又不像 config.yml 那样显眼，极难排查。
         * 收在 core 里而不是两个壳各写一遍，保证判定口径只有一处。
         */
        fun connectionSeed(
            endpoints: List<String>,
            bootstrapToken: String,
        ): Pair<String, String> {
            val endpoint = endpoints.firstOrNull { it.isNotBlank() && !looksLikePlaceholder(it) }.orEmpty()
            val token = if (bootstrapToken.isNotBlank() && !looksLikePlaceholder(bootstrapToken)) bootstrapToken else ""
            return endpoint to token
        }
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
        // 只有 config.yml 填了**真实**接入信息才压过凭据；占位符等同于未配置，
        // 否则 join 写入的凭据会被模板占位符永远盖住（join 静默失效的根因）。
        if (hasRealConnectionConfig(settings)) {
            return settings
        }
        return settings.copy(endpoints = listOf(credentials.endpoint), bootstrapToken = credentials.token)
    }
}

/**
 * 是否为模板占位符（`<CONTROL_PLANE_URL>` / `${VAR}` 这类整串包裹的写法）。
 *
 * 判据刻意保守：只认「整串被尖括号或 `${}` 包住」，避免把真实地址里的字符误判成占位符。
 * 放在文件顶层供实例方法与 [EffectiveSettingsResolver.connectionSeed] 共用，保证只有一处口径。
 */
private fun looksLikePlaceholder(raw: String): Boolean {
    val text = raw.trim()
    if (text.length < 3) {
        return false
    }
    return (text.startsWith("<") && text.endsWith(">")) ||
        (text.startsWith("\${") && text.endsWith("}"))
}
