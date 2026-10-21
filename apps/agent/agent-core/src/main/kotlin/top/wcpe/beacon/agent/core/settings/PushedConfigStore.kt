package top.wcpe.beacon.agent.core.settings

import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.nio.charset.StandardCharsets
import java.nio.file.AtomicMoveNotSupportedException
import java.nio.file.Files
import java.nio.file.Path
import java.nio.file.StandardCopyOption
import java.time.Clock
import java.time.Instant

/** 下发配置的本地缓存文件名。 */
private const val PUSHED_CONFIG_FILE_NAME = "pushed-config.json"

/** 缓存格式版本。 */
private const val PUSHED_CONFIG_FORMAT_VERSION = 1

/**
 * Web 下发配置的本地缓存。
 *
 * ## 为什么必须落盘
 *
 * 下发配置是「控制面权威」的，但**权威不等于随时可达**：agent 重启、控制面维护、
 * 网络抖动都会让那次拉取失败。如果只在内存里，断连后就会退回 config.yml 或代码默认，
 * 表现为「配置莫名其妙变了」——对交付类操作这不可接受（重载超时会从 30s 退回 0，
 * 等于悄悄关掉保护）。
 *
 * 落盘后语义变成：**最后一次已知的权威值**，断连时继续用它工作。
 *
 * ## 与另外两类文件的分工
 *
 * - `config.yml`：运维手写的部署参数，**离线兜底**
 * - `agent-credentials.json`：身份（地址 + 令牌），**专用于接入**
 * - `pushed-config.json`（本类）：Web 下发的值，**最高优先级**
 *
 * 三者分开存，是为了让「这个值是哪来的」永远可回答——排查配置问题时不用猜。
 *
 * @property values 键 → 值（键名与 config.yml 一致，如 `delivery.reload-pool-size`）
 * @property namespace 下发时的 namespace（用于判断缓存是否还对应本机当前归属）
 * @property serverId 下发时的 serverId
 * @property fetchedAt 拉取时刻（UTC），供 status 命令展示「这份缓存有多旧」
 */
data class PushedConfig(
    val values: Map<String, String>,
    val namespace: String,
    val serverId: String,
    val fetchedAt: Instant?,
) {
    companion object {
        /** 空缓存：尚未成功拉取过。 */
        val EMPTY = PushedConfig(emptyMap(), "", "", null)
    }
}

/**
 * 下发配置缓存的读写。
 *
 * 与 [AgentCredentialsStore][top.wcpe.beacon.agent.core.identity.AgentCredentialsStore]
 * 一样走 [JsonCodec]，不在 core 手写解析：字段虽简单，但转义与类型判断交给成熟实现更稳。
 */
class PushedConfigStore(
    private val dataDir: Path,
    private val codec: JsonCodec,
    private val clock: Clock = Clock.systemUTC(),
) {
    private val file: Path = dataDir.resolve(PUSHED_CONFIG_FILE_NAME)

    /** 缓存文件路径（供诊断提示用）。 */
    fun filePath(): Path = file

    /** 读取缓存；不存在或损坏返回 [PushedConfig.EMPTY]（等价「没下发过」，不是错误）。 */
    fun load(): PushedConfig {
        if (!Files.exists(file)) {
            return PushedConfig.EMPTY
        }
        val text = runCatching { Files.readString(file, StandardCharsets.UTF_8) }.getOrNull() ?: return PushedConfig.EMPTY
        // 损坏只意味着「没有下发值可用」，绝不能抛异常阻止启动。
        return runCatching { decode(text) }.getOrDefault(PushedConfig.EMPTY)
    }

    /**
     * 写入缓存。
     *
     * 原子写：损坏缓存虽然会被 [load] 降级成空，但那等于**悄悄丢失下发配置**，
     * 比写失败更糟，故必须原子。
     */
    fun save(config: PushedConfig) {
        Files.createDirectories(dataDir)
        val tmp = dataDir.resolve("$PUSHED_CONFIG_FILE_NAME.tmp")
        Files.writeString(tmp, codec.encode(encode(config)), StandardCharsets.UTF_8)
        try {
            Files.move(tmp, file, StandardCopyOption.ATOMIC_MOVE, StandardCopyOption.REPLACE_EXISTING)
        } catch (_: AtomicMoveNotSupportedException) {
            Files.move(tmp, file, StandardCopyOption.REPLACE_EXISTING)
        }
    }

    /** 用新拉到的值更新缓存（记录当前时刻，供诊断缓存新鲜度）。 */
    fun update(
        values: Map<String, String>,
        namespace: String,
        serverId: String,
    ): PushedConfig = PushedConfig(values, namespace, serverId, Instant.now(clock)).also { save(it) }

    private fun encode(c: PushedConfig): Map<String, Any?> =
        linkedMapOf(
            "formatVersion" to PUSHED_CONFIG_FORMAT_VERSION,
            "namespace" to c.namespace,
            "serverId" to c.serverId,
            "fetchedAt" to c.fetchedAt?.toString(),
            "values" to c.values.toSortedMap(),
        )

    @Suppress("UNCHECKED_CAST")
    private fun decode(text: String): PushedConfig {
        val root = codec.decode(text) as? Map<String, Any?> ?: return PushedConfig.EMPTY
        val version = (root["formatVersion"] as? Number)?.toInt()
        if (version != PUSHED_CONFIG_FORMAT_VERSION) {
            return PushedConfig.EMPTY
        }
        // 只保留字符串值：下发值一律以字符串承载，非字符串视为异常数据丢弃。
        val rawValues = root["values"] as? Map<String, Any?> ?: emptyMap()
        val values =
            rawValues.entries
                .mapNotNull { (k, v) -> (v as? String)?.let { k to it } }
                .toMap(LinkedHashMap())
        return PushedConfig(
            values = values,
            namespace = root["namespace"] as? String ?: "",
            serverId = root["serverId"] as? String ?: "",
            fetchedAt = (root["fetchedAt"] as? String)?.let { runCatching { Instant.parse(it) }.getOrNull() },
        )
    }
}
