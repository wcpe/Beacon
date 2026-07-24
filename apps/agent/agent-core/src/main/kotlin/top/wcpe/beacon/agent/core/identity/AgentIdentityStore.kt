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

    fun loadExisting(): StoredAgentIdentity {
        if (!Files.exists(file)) {
            return StoredAgentIdentity("", null, isValid = false, error = "身份文件不存在")
        }
        val fields = parseFields(Files.readAllLines(file, StandardCharsets.UTF_8))
        val formatVersion = fields["format-version"]
        val identityId = fields["identity-id"] ?: ""
        val createdAt = fields["created-at"]?.let { parseInstantOrNull(it) }
        return when {
            formatVersion != IDENTITY_FORMAT_VERSION -> invalid("身份文件格式版本不支持")
            !isUuidV4(identityId) -> invalid("身份标识不是合法 UUIDv4")
            createdAt == null -> invalid("身份文件缺少合法生成时间")
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

    private fun invalid(message: String): StoredAgentIdentity = StoredAgentIdentity("", null, isValid = false, error = message)

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
