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

    /** 从配置读取 AgentSettings。 */
    fun readSettings(reader: ConfigReader): AgentSettings {
        return AgentSettings(
            endpoints = reader.stringList("beacon.endpoints"),
            bootstrapToken = reader.string("beacon.bootstrap-token", ""),
            pollTimeoutMs = reader.long("timing.poll-timeout-ms", 30000),
            requestTimeoutMs = reader.long("timing.request-timeout-ms", 5000),
            heartbeatFallbackMs = reader.long("timing.heartbeat-fallback-ms", 10000),
            backoff = BackoffSettings(
                initialMs = reader.long("backoff.initial-ms", 1000),
                maxMs = reader.long("backoff.max-ms", 30000),
                multiplier = reader.double("backoff.multiplier", 2.0),
                jitterRatio = reader.double("backoff.jitter-ratio", 0.2),
            ),
            snapshotEnabled = reader.boolean("snapshot.enabled", true),
            snapshotFileName = reader.string("snapshot.file-name", "effective-config.snapshot.json"),
            fileTree = FileTreeSettings(
                enabled = reader.boolean("file-tree.enabled", true),
                targetSubDir = reader.string("file-tree.target-sub-dir", ""),
                appliedManifestFileName = reader.string("file-tree.applied-manifest-file-name", "file-tree.applied.json"),
            ),
            override = OverrideSettings(
                // 命令白名单本地配置、默认空（控制面不下发；空即命令派发能力关闭，见 ADR-0011 决策 3）。
                commandWhitelist = reader.stringList("override.command-whitelist").toSet(),
                backupDirName = reader.string("override.backup-dir-name", "override-backup"),
            ),
            messaging = MessagingSettings(
                // 跨服消息模块默认关（ADR-0016 决策 6）；Redis 连接由控制面下发，不在本地。
                enabled = reader.boolean("messaging.enabled", false),
                rpcTimeoutMs = reader.long("messaging.rpc-timeout-ms", 5000),
                streamMaxLen = reader.long("messaging.stream-max-len", 10000),
                consumerName = reader.string("messaging.consumer-name", "default"),
            ),
            proxy = ProxySettings(
                // BC 代理服务的大区 / 小区（FR-48）；默认空 = 未配，默认服走兜底首个在线子服。bukkit 不读此项。
                homeGroup = reader.string("proxy.home-group", ""),
                homeZone = reader.string("proxy.home-zone", ""),
            ),
        )
    }

    /**
     * 从配置读取 AgentIdentity。
     *
     * @param role 壳层按平台固定传入（bukkit / bungee），覆盖配置中的 role。
     */
    fun readIdentity(reader: ConfigReader, role: String): AgentIdentity {
        val metadata = LinkedHashMap<String, String>()
        for (key in reader.keys("identity.metadata")) {
            metadata[key] = reader.string("identity.metadata.$key", "")
        }
        return AgentIdentity(
            namespace = reader.string("identity.namespace", ""),
            serverId = reader.string("identity.server-id", ""),
            role = role,
            groupHint = reader.string("identity.group-hint", ""),
            address = reader.string("identity.address", ""),
            version = reader.string("identity.version", ""),
            capacity = reader.int("identity.capacity", 0),
            weight = reader.int("identity.weight", 0),
            metadata = metadata,
        )
    }
}
