package top.wcpe.beacon.agent.adapters.testutil

import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.settings.AgentSettings
import top.wcpe.beacon.agent.core.settings.BackoffSettings

/** 测试共用的 settings / identity 构造。 */
object TestFixtures {

    fun settings(): AgentSettings = AgentSettings(
        endpoints = listOf("http://127.0.0.1:8080"),
        bootstrapToken = "test-token",
        pollTimeoutMs = 30000,
        requestTimeoutMs = 5000,
        heartbeatFallbackMs = 10000,
        backoff = BackoffSettings(1000, 30000, 2.0, 0.2),
        snapshotEnabled = true,
        snapshotFileName = "snap.json",
    )

    fun identity(): AgentIdentity = AgentIdentity(
        namespace = "prod",
        serverId = "lobby-1",
        role = "bukkit",
        groupHint = "area1",
        address = "10.0.0.7:25565",
        version = "1.4.2",
        capacity = 200,
        weight = 100,
        metadata = mapOf("region" to "cn-east"),
    )
}
