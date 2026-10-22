package top.wcpe.beacon.agent.core.identity

import java.nio.charset.StandardCharsets
import java.nio.file.AtomicMoveNotSupportedException
import java.nio.file.Files
import java.nio.file.Path
import java.nio.file.StandardCopyOption
import java.time.Clock
import java.time.Instant
import java.util.Locale
import java.util.UUID

private const val IDENTITY_FILE_NAME = "identity.yml"
private const val IDENTITY_FORMAT_VERSION = "1"

data class StoredAgentIdentity(
    val identityId: String,
    val createdAt: Instant?,
    val isValid: Boolean,
    val error: String = "",
)

class AgentIdentityStore(
    private val dataDir: Path,
    private val clock: Clock = Clock.systemUTC(),
) {
    private val file: Path = dataDir.resolve(IDENTITY_FILE_NAME)

    fun loadOrCreate(): StoredAgentIdentity {
        val existing = loadExisting()
        if (existing.isValid || Files.exists(file)) {
            return existing
        }
        return createNew()
    }

    /**
     * 让身份文件与给定身份保持一致，**绝不新建第二个身份**。
     *
     * ## 为什么需要它
     *
     * 凭据文件（`agent-credentials.json`）才是身份的单一真源，本文件只是历史遗留的副本。
     * 但如果壳层在「凭据已权威」时仍调 [loadOrCreate]，文件缺失就会落盘一个从未在控制面
     * 注册过的新 UUID B：正常启动用凭据里的 A 接入（B 不参与），可一旦凭据变得不可读，
     * 迁移会立刻把 B 复活成身份——控制面看到的是「又来了一台新服务器」。
     *
     * ## 取舍：为什么选「覆写成同一 ID」而不是「只 loadExisting 不落盘」
     *
     * 后者确实不再产生新 UUID，但治不了**已经存在**的那个休眠 UUID：升级过的机器上它
     * 已经躺在数据目录里，只要凭据坏一次就会被复活。覆写把它就地纠正成权威身份，
     * 两份文件从此永远一致，迁移路径再也不会复活第二个身份。
     * 代价是本文件不再保留「本机最初生成的身份」——但那个身份从未参与接入，留着只有害处。
     *
     * 身份相同则不写盘（幂等），避免每次启动都抖一次时间戳。
     *
     * @param identityId 权威身份（来自凭据或旧文件迁移）；不是合法 UUIDv4 时不动文件
     * @return 文件中最终承载的身份；未写入时返回现有读取结果
     */
    fun alignTo(identityId: String): StoredAgentIdentity {
        val existing = loadExisting()
        return if (!isUuidV4(identityId) || (existing.isValid && existing.identityId == identityId)) {
            existing
        } else {
            Files.createDirectories(dataDir)
            // created-at 记为本次对齐时刻：它描述的是「这份文件何时写成」，不是最初那次首启。
            val alignedAt = Instant.now(clock)
            writeAtomically(render(identityId, alignedAt))
            StoredAgentIdentity(identityId, alignedAt, isValid = true)
        }
    }

    fun loadExisting(): StoredAgentIdentity {
        if (!Files.exists(file)) {
            return StoredAgentIdentity("", null, isValid = false, error = "身份文件不存在")
        }
        val fields = parseFields(Files.readAllLines(file, StandardCharsets.UTF_8))
        val formatVersion = fields["format-version"]
        val identityId = fields["identity-id"] ?: ""
        val createdAt = fields["created-at"]?.let { parseInstantOrNull(it) }
        return when {
            formatVersion != IDENTITY_FORMAT_VERSION -> invalidIdentity("身份文件格式版本不支持")
            !isUuidV4(identityId) -> invalidIdentity("身份标识不是合法 UUIDv4")
            createdAt == null -> invalidIdentity("身份文件缺少合法生成时间")
            else -> StoredAgentIdentity(identityId, createdAt, isValid = true)
        }
    }

    private fun createNew(): StoredAgentIdentity {
        Files.createDirectories(dataDir)
        val identityId = UUID.randomUUID().toString().lowercase(Locale.ROOT)
        val createdAt = Instant.now(clock)
        writeAtomically(render(identityId, createdAt))
        return StoredAgentIdentity(identityId, createdAt, isValid = true)
    }

    private fun writeAtomically(content: String) {
        val tmp = dataDir.resolve("$IDENTITY_FILE_NAME.tmp")
        Files.writeString(tmp, content, StandardCharsets.UTF_8)
        try {
            Files.move(tmp, file, StandardCopyOption.ATOMIC_MOVE, StandardCopyOption.REPLACE_EXISTING)
        } catch (_: AtomicMoveNotSupportedException) {
            Files.move(tmp, file, StandardCopyOption.REPLACE_EXISTING)
        }
    }

    private fun parseFields(lines: List<String>): Map<String, String> {
        return lines
            .asSequence()
            .map { it.trim() }
            .filter { it.isNotEmpty() && !it.startsWith("#") }
            .mapNotNull { parseField(it) }
            .toMap(LinkedHashMap())
    }

    private fun parseField(line: String): Pair<String, String>? {
        val index = line.indexOf(':')
        if (index <= 0) {
            return null
        }
        val key = line.substring(0, index).trim()
        val raw = line.substring(index + 1).trim()
        return key to raw.trim('"')
    }

    private fun render(
        identityId: String,
        createdAt: Instant,
    ): String =
        """
        |# Beacon agent 身份文件。首次启动自动生成，唯一标识本服务器实例。
        |# 严禁手工修改或复制到其他服务器目录；换机迁移时须随数据目录整体迁移。
        |# 身份文件格式版本
        |format-version: 1
        |# 本 agent 的唯一身份标识（UUIDv4，首启生成后终身不变）
        |identity-id: "$identityId"
        |# 生成时刻（UTC）
        |created-at: "$createdAt"
        |
        """.trimMargin()

    private fun parseInstantOrNull(raw: String): Instant? =
        try {
            Instant.parse(raw)
        } catch (_: RuntimeException) {
            null
        }

    private fun isUuidV4(raw: String): Boolean =
        try {
            UUID.fromString(raw).version() == 4
        } catch (_: IllegalArgumentException) {
            false
        }
}

/**
 * 解析失败时的「空身份」。
 *
 * 放在类外（顶层私有）而不是成员：本类已贴着 detekt 的类内函数数上限，
 * 而它不碰任何实例状态，只把一个错误原因包成「无效身份」
 * （与 `ReloadDispatcher` 里的 `sanitizeForReceipt` 同一处理方式，不用 @Suppress）。
 */
private fun invalidIdentity(message: String): StoredAgentIdentity = StoredAgentIdentity("", null, isValid = false, error = message)
