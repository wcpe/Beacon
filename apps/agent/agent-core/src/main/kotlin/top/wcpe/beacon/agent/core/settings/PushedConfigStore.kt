package top.wcpe.beacon.agent.core.settings

import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.nio.charset.StandardCharsets
import java.nio.file.AtomicMoveNotSupportedException
import java.nio.file.Files
import java.nio.file.Path
import java.nio.file.StandardCopyOption
import java.time.Clock
import java.time.Instant
import java.util.concurrent.atomic.AtomicReference

/** 下发配置的本地缓存文件名。 */
private const val PUSHED_CONFIG_FILE_NAME = "pushed-config.json"

/** 缓存格式版本。 */
private const val PUSHED_CONFIG_FORMAT_VERSION = 1

/**
 * 缓存不可用的**原因分类**（FR-277 #6：降级到 config.yml **并告警**）。
 *
 * ## 为什么要分类，不只报「读不出来」
 *
 * 三种原因的**处置方式完全不同**，笼统报错了等于让运维去猜：
 * 内容损坏 → 删文件重建即可，代价只是等下一次拉取；
 * 版本不支持 → 是**更高版本 agent** 写的缓存，删了就丢掉已有下发值，该升级 agent 而不是删；
 * 读取失败 → 文件被占用 / 权限问题，删文件往往也没用，得看权限。
 *
 * @property label 面向运维的原因描述
 * @property advice 该原因的处置建议（告警文案末尾会带上）
 */
internal enum class PushedConfigDegradation(
    val label: String,
    val advice: String,
) {
    /** 文件存在但读不出来（占用 / 权限 / 磁盘错误）。 */
    UNREADABLE(
        label = "缓存文件读取失败（可能被占用或权限不足）",
        advice = "请检查该文件的权限；确认无需保留后可删除它让 agent 重新写入",
    ),

    /** 文件能读但解析不出来（手工改坏 / 写入中断 / 非 JSON 内容）。 */
    BROKEN(
        label = "缓存文件内容损坏，无法解析",
        advice = "删除该文件即可让 agent 重新写入（下发值会在下一次拉取后恢复）",
    ),

    /** 能解析但格式版本不是本版本认识的（通常由更高版本 agent 写入）。 */
    UNSUPPORTED_VERSION(
        label = "缓存格式版本不受支持（该文件可能由更高版本的 agent 写入）",
        advice = "请升级 agent，不要删除该文件（删除会丢掉已有的下发值）",
    ),
}

/**
 * [PushedConfigStore.load] 的一次读取结果。
 *
 * @property config 读到的缓存（降级时为空缓存）
 * @property degradation 降级原因；正常或「文件不存在」时为 null
 */
private data class CacheRead(
    val config: PushedConfig,
    val degradation: PushedConfigDegradation?,
)

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

    /**
     * 上次已上报的降级原因（去重状态）。
     *
     * 存**原因**而不是布尔值，是为了让「原因变了」这件事还能再报一次：
     * 同一份坏文件被周期读取时只报第一次，但从「内容损坏」变成「版本不支持」值得让人知道。
     */
    private val reported = AtomicReference<PushedConfigDegradation?>(null)

    /** 缓存文件路径（供诊断提示用）。 */
    fun filePath(): Path = file

    /**
     * 读取缓存；不存在或损坏返回 [PushedConfig.EMPTY]（等价「没下发过」，不是错误）。
     *
     * ## 降级可见但不致命（FR-277 #6）
     *
     * 读不出来**绝不抛异常、绝不让启动失败**——老行为原样保留。但也**不能静默**：
     * 传了 [onDegraded] 就把原因上报一次，运维才分得清「这台机器没被下发过」和「缓存坏了」。
     * 不传 [onDegraded] 的调用点行为与旧版完全一致（读不出就给空），故本参数是纯加性演进。
     *
     * 去重规则见 [report]：同一原因只报一次，周期读取不会刷屏。
     *
     * @param onDegraded 降级告警出口（参数为面向运维的完整告警文案）；null 表示不上报
     */
    fun load(onDegraded: ((String) -> Unit)? = null): PushedConfig {
        val cache = readCache()
        report(cache.degradation, onDegraded)
        return cache.config
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

    /**
     * 读取并区分「文件不存在 / 读取失败 / 内容损坏 / 版本不支持 / 正常」。
     *
     * 不存在的分支**不产生降级原因**：首次启动本来就没有缓存，那不是故障，
     * 若也告警，等于每台新机开机先报一次假警，很快就会被无视。
     */
    private fun readCache(): CacheRead {
        if (!Files.exists(file)) {
            return CacheRead(PushedConfig.EMPTY, null)
        }
        val text = runCatching { Files.readString(file, StandardCharsets.UTF_8) }.getOrNull()
        return text?.let(::decodeChecked) ?: CacheRead(PushedConfig.EMPTY, PushedConfigDegradation.UNREADABLE)
    }

    /**
     * 解析缓存文本，失败时给出**可区分**的原因。
     *
     * 与旧实现一样不抛异常：损坏只意味着「没有下发值可用」，绝不能阻止启动。
     *
     * 用 `when` 而不是连续提前返回，是为了让「三类失败 + 正常」在同一个表达式里一眼看全：
     * 各分支返回的是同一种 [CacheRead]，读的人不必顺着多个出口拼状态。
     */
    @Suppress("UNCHECKED_CAST")
    private fun decodeChecked(text: String): CacheRead {
        val root = runCatching { codec.decode(text) }.getOrNull() as? Map<String, Any?>
        val version = (root?.get("formatVersion") as? Number)?.toInt()
        return when {
            // JSON 语法错、顶层不是对象：两者都等于「这份缓存读不出来」。
            root == null -> CacheRead(PushedConfig.EMPTY, PushedConfigDegradation.BROKEN)
            version != PUSHED_CONFIG_FORMAT_VERSION -> CacheRead(PushedConfig.EMPTY, PushedConfigDegradation.UNSUPPORTED_VERSION)
            else -> CacheRead(fromRoot(root), null)
        }
    }

    /**
     * 降级上报（同一原因去重）。
     *
     * ## 去重怎么做的
     *
     * 记住「上次上报的原因」，只有原因**发生变化**才再报：
     * - 同一份坏文件被周期读取（30s 一次 × 24h ≈ 2880 次）→ 只报第一句
     * - 原因从「内容损坏」变成「版本不支持」→ 再报一次（情况真的变了）
     * - 恢复正常（原因为 null）→ 只清记录、不告警（修好了没必要打扰），之后真坏了还能再报
     *
     * @param degradation 本次的降级原因；null 表示正常
     * @param onDegraded 告警出口；null 表示本次不上报（也不记状态）
     */
    private fun report(
        degradation: PushedConfigDegradation?,
        onDegraded: ((String) -> Unit)?,
    ) {
        if (onDegraded == null) {
            return
        }
        if (reported.getAndSet(degradation) == degradation || degradation == null) {
            return
        }
        onDegraded.invoke(
            "下发配置缓存不可用：${degradation.label}，已降级到 config.yml / 代码默认值（本机的下发项暂不生效）。" +
                "缓存文件：$file。建议：${degradation.advice}",
        )
    }

    /** 从已通过版本校验的根对象还原缓存。 */
    private fun fromRoot(root: Map<String, Any?>): PushedConfig {
        // 只保留字符串值：下发值一律以字符串承载，非字符串视为异常数据丢弃。
        val rawValues = root["values"] as? Map<*, *> ?: emptyMap<Any?, Any?>()
        val values = LinkedHashMap<String, String>()
        for ((key, value) in rawValues) {
            if (key is String && value is String) {
                values[key] = value
            }
        }
        return PushedConfig(
            values = values,
            namespace = root["namespace"] as? String ?: "",
            serverId = root["serverId"] as? String ?: "",
            fetchedAt = (root["fetchedAt"] as? String)?.let { runCatching { Instant.parse(it) }.getOrNull() },
        )
    }
}
