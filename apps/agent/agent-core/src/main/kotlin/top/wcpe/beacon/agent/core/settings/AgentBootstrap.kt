package top.wcpe.beacon.agent.core.settings

import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.messaging.MessagingSettings

/**
 * 从 ConfigReader 构造 AgentSettings + AgentIdentity（config.yml → core 类型）。
 *
 * 字段与 docs/API.md「agent 侧」对齐：metadata 仅 map<string,string>，无 zone、无 canary。
 * role 由壳层按平台固定传入。
 */
object AgentBootstrap {
    /** `assets.scan-interval-sec` 的下限（秒，规格 §4.2）：低于下限一律收口，防过频扫描踩踏。 */
    private val scanIntervalRange = 300L..Long.MAX_VALUE

    /** 重载超时的合法区间：0 = 不设超时（运维显式关掉保护的唯一写法），上限与控制面口径一致。 */
    private val reloadTimeoutRange = 0L..PushedConfigCoordinator.MAX_RELOAD_TIMEOUT_MS

    /**
     * 并发重载数上限的合法区间：0/负数会让池无法工作，过大则放大卡死线程的泄漏面（线程无法强杀）。
     *
     * 上下界取自 [PushedConfigCoordinator]：下发路径与本地配置**必须同一口径**——
     * 任一处放松，就会出现「Web 上显示 256、本地却按 800 建线程」这类对不上的状态。
     */
    private val reloadPoolRange = PushedConfigCoordinator.MIN_RELOAD_POOL_SIZE..PushedConfigCoordinator.MAX_RELOAD_POOL_SIZE

    /**
     * 从配置读取 AgentSettings。
     *
     * @param onClamped 越界收口时的告警出口（FR-280 #3）。为 null 表示不告警（纯读取 / 测试场景）。
     *                  **收口本身不抛异常、不影响启动**，但必须让人看得见：手写了越界值却毫无提示，
     *                  等于配置被悄悄改掉，运维会照着自己填的值去排错，方向从一开始就是错的。
     */
    fun readSettings(
        reader: ConfigReader,
        onClamped: ((String) -> Unit)? = null,
    ): AgentSettings {
        return AgentSettings(
            endpoints = reader.stringList("beacon.endpoints"),
            bootstrapToken = reader.string("beacon.bootstrap-token", ""),
            pollTimeoutMs = reader.long("timing.poll-timeout-ms", 30000),
            requestTimeoutMs = reader.long("timing.request-timeout-ms", 5000),
            heartbeatFallbackMs = reader.long("timing.heartbeat-fallback-ms", 10000),
            backoff =
                BackoffSettings(
                    initialMs = reader.long("backoff.initial-ms", 1000),
                    maxMs = reader.long("backoff.max-ms", 30000),
                    multiplier = reader.double("backoff.multiplier", 2.0),
                    jitterRatio = reader.double("backoff.jitter-ratio", 0.2),
                ),
            snapshotEnabled = reader.boolean("snapshot.enabled", true),
            snapshotFileName = reader.string("snapshot.file-name", "effective-config.snapshot.json"),
            fileTree =
                FileTreeSettings(
                    enabled = reader.boolean("file-tree.enabled", true),
                    targetSubDir = reader.string("file-tree.target-sub-dir", ""),
                    appliedManifestFileName = reader.string("file-tree.applied-manifest-file-name", "file-tree.applied.json"),
                ),
            override =
                OverrideSettings(
                    // 命令白名单本地配置、默认空（控制面不下发；空即命令派发能力关闭，见 ADR-0011 决策 3）。
                    commandWhitelist = reader.stringList("override.command-whitelist").toSet(),
                    backupDirName = reader.string("override.backup-dir-name", "override-backup"),
                ),
            messaging =
                MessagingSettings(
                    // 跨服消息模块默认关（ADR-0016 决策 6）；Redis 连接由控制面下发，不在本地。
                    enabled = reader.boolean("messaging.enabled", false),
                    rpcTimeoutMs = reader.long("messaging.rpc-timeout-ms", 5000),
                ),
            proxy =
                ProxySettings(
                    // BC 代理服务的大区 / 小区（FR-48）；默认空 = 未配，默认服走兜底首个在线子服。bukkit 不读此项。
                    homeGroup = reader.string("proxy.home-group", ""),
                    homeZone = reader.string("proxy.home-zone", ""),
                ),
            assets =
                AssetIndexSettings(
                    // 是否启用文件资产索引（FR-163）；默认启用。
                    enabled = reader.boolean("assets.enabled", true),
                    // 周期扫描间隔（秒），默认 1800（30 分钟）；下限 300（规格 §4.2）。
                    scanIntervalSec = clampLong(reader, "assets.scan-interval-sec", 1800L, scanIntervalRange, onClamped),
                    // 本地清单缓存文件名（落 agent 数据目录）。
                    manifestFileName = reader.string("assets.manifest-file-name", "asset-manifest.json"),
                ),
            // 单业务插件的配置重载超时（毫秒，FR-274）。默认 30000：
            // 默认不设超时（0）会让「插件卡死」退化成「永远等不到结论」——正是本功能要消灭的形态。
            // 须**小于控制面的 activateTimeoutSec**，否则控制面先判整体超时，回执里看不出是哪个插件。
            // 上限与热更新路径（PushedConfigCoordinator.MAX_RELOAD_TIMEOUT_MS）一致：
            // 本地配置同样能把 deadline 推到溢出，两条路径口径必须相同。
            configReloadTimeoutMs =
                clampLong(reader, PushedConfigCoordinator.KEY_RELOAD_TIMEOUT_MS, 30_000L, reloadTimeoutRange, onClamped),
            // 并发重载的插件数上限（FR-280）。收口到 [1, 256]（见 reloadPoolRange）。
            configReloadPoolSize = clampInt(reader, PushedConfigCoordinator.KEY_RELOAD_POOL_SIZE, 64, reloadPoolRange, onClamped),
        )
    }

    /**
     * 读取 long 型配置，越界则收口到 [range] 并告警（FR-280 #3）。
     *
     * 三个「必须收口」的配置项共用本方法，收口规则与告警文案因此只有一处定义——
     * 否则新增一项时极易只补了收口、忘了告警（正是本组要修的缺口）。
     *
     * @return 收口后的生效值（**永远合法**，绝不抛异常）
     */
    private fun clampLong(
        reader: ConfigReader,
        key: String,
        default: Long,
        range: LongRange,
        onClamped: ((String) -> Unit)?,
    ): Long {
        val raw = reader.long(key, default)
        val fixed = raw.coerceIn(range)
        if (raw != fixed) {
            onClamped?.invoke(clampMessage(key, raw, fixed, rangeLabel(range.first, range.last)))
        }
        return fixed
    }

    /**
     * 读取 int 型配置，越界则收口到 [range] 并告警（FR-280 #3）。
     *
     * 与 [clampLong] 并列而不合并成泛型：`ConfigReader` 的 `int` / `long` 是两个独立入口
     * （平台实现对二者的转换行为不同），合并会引入一次「先 long 再 toInt」的隐式收窄。
     */
    private fun clampInt(
        reader: ConfigReader,
        key: String,
        default: Int,
        range: IntRange,
        onClamped: ((String) -> Unit)?,
    ): Int {
        val raw = reader.int(key, default)
        val fixed = raw.coerceIn(range)
        if (raw != fixed) {
            onClamped?.invoke(clampMessage(key, raw, fixed, rangeLabel(range.first.toLong(), range.last.toLong())))
        }
        return fixed
    }

    /** 合法区间的可读文案；上限为 [Long.MAX_VALUE] 时只说下限（避免打印一串天文数字）。 */
    private fun rangeLabel(
        min: Long,
        max: Long,
    ): String = if (max == Long.MAX_VALUE) "≥$min" else "$min~$max"

    /**
     * 收口告警文案。
     *
     * 四个要素缺一不可：**键名**（改哪个）、**原值**（现在写的是什么）、
     * **生效值**（实际按什么跑）、**合法区间**（该改成什么范围）。
     * 只说「配置越界」的告警等于没说，运维仍得自己去翻代码找边界。
     */
    private fun clampMessage(
        key: String,
        raw: Number,
        fixed: Number,
        rangeText: String,
    ): String = "配置值越界已收口：$key=$raw 超出合法区间 $rangeText，实际生效 $fixed（请修正 config.yml 或该键的下发值）"

    /**
     * 从配置读取 AgentIdentity。
     *
     * @param role 壳层按平台固定传入（bukkit / bungee），覆盖配置中的 role。
     * @param agentVersion agent 自身构建版本（FR-86，见 ADR-0039）：壳层经 TabooLib pluginVersion 注入、
     *                     非从 config.yml 读（构建版本非运维可配）；默认空兼容旧调用点。
     */
    fun readIdentity(
        role: String,
        agentVersion: String = "",
        serverWorkDir: String = "",
    ): AgentIdentity {
        return AgentIdentity(
            // namespace / serverId 只接受控制面确认；旧键仅供迁移兼容，不能覆盖运行期绑定。
            namespace = "",
            serverId = "",
            role = role,
            groupHint = "",
            address = "",
            version = "",
            capacity = 0,
            weight = 0,
            metadata = emptyMap(),
            agentVersion = agentVersion,
            serverWorkDir = serverWorkDir,
        )
    }
}
