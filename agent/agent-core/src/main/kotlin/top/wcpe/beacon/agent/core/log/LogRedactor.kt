package top.wcpe.beacon.agent.core.log

/**
 * 日志脱敏纯函数（FR-88，见 ADR-0040）。无 IO、无副作用，便于穷举单测。
 *
 * 在日志行**落环形缓冲那一刻**调用：把常见敏感键（token / password / secret / authorization /
 * bootstrap-token 等）后跟的值替换为掩码 [MASK]，使缓冲、回传、控制面瞬态、前端展示全链路只见脱敏文本。
 *
 * 设计取舍：**只针对明确的敏感键名**做「键 + 分隔符 + 值」掩码，不做泛化的「任意 key=value 都掩」——
 * 否则会误伤 `md5=abc`、`group=area1` 等正常运维事实（这正是单测守的边界）。保守倾向：宁可只掩已知敏感键，
 * 不冒误掩正常字段的风险；敏感键集合可按需扩充。
 */
object LogRedactor {

    /** 掩码占位符。 */
    private const val MASK = "***"

    /**
     * 敏感键名（小写、不含分隔符）。命中「键 + (= 或 :) + 值」即把值掩码。
     *
     * 不加 `\b` 词边界：键名作子串命中亦掩码（如 `mytoken=val` 会命中尾部 `token=val` 而掩码）。
     * 这是有意为之——脱敏宁严勿松，过度掩码（多掩了一个本不敏感的值）无害，漏掩才会泄露。
     * 带连字符的整键（如 bootstrap-token / x-beacon-token）照常整体命中。
     */
    private val SENSITIVE_KEYS = listOf(
        "bootstrap-token",
        "x-beacon-token",
        "authorization",
        "password",
        "passwd",
        "secret",
        "token",
        "apikey",
        "api-key",
        "credential",
    )

    /**
     * 敏感键 + 分隔符 + 值 的匹配正则（大小写不敏感）。
     *
     * 分组1=键与分隔符（含尾随空白）原样保留，分组2=值（非空白连续串，可含 `Bearer ` 前缀的实际令牌部分）被掩码。
     * 例：`bootstrap-token=abc` / `password: hunter2` / `Authorization: Bearer eyJ...`。
     */
    private val keyValuePattern: Regex = run {
        val keys = SENSITIVE_KEYS.joinToString("|") { Regex.escape(it) }
        // 键（无词边界，子串命中亦掩码、宁严勿松）+ 空白* + 分隔符(=|:) + 空白* + 可选 Bearer 前缀 + 值（连续非空白）
        Regex(
            "(?i)((?:$keys)\\s*[:=]\\s*(?:Bearer\\s+)?)(\\S+)",
        )
    }

    /**
     * 脱敏一行日志文本：把敏感键后的值替换为掩码，其余原样返回。
     *
     * @param line 原始日志文本
     * @return 脱敏后的文本（无敏感键命中时与入参相等）
     */
    fun redact(line: String): String {
        return keyValuePattern.replace(line) { m ->
            m.groupValues[1] + MASK
        }
    }
}
