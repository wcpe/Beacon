package com.beacon.agent.core.settings

import com.beacon.agent.core.identity.AgentIdentity

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
