package top.wcpe.beacon.agent.core.identity

import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.nio.charset.StandardCharsets
import java.nio.file.AtomicMoveNotSupportedException
import java.nio.file.Files
import java.nio.file.Path
import java.nio.file.StandardCopyOption
import java.nio.file.attribute.PosixFilePermission
import java.time.Instant
import java.util.Locale
import java.util.UUID

/** 身份凭据文件名。与 config.yml 分离，避免「凭据混进配置」被误提交或误读。 */
private const val CREDENTIALS_FILE_NAME = "agent-credentials.json"

/** 凭据格式版本：后续字段演进靠它识别，读到未知版本即拒绝而不是猜。 */
private const val CREDENTIALS_FORMAT_VERSION = 1

/**
 * 一份 agent 身份凭据：**接入所需的全部事实**都在这里。
 *
 * ## 为什么单独成文件而不放 config.yml
 *
 * config.yml 是「运维手写的部署参数」，会被纳入版本管理、被模板复制、被批量分发；
 * 而凭据是**每台服务器唯一且保密**的。两者混在一起的实际后果是：模板复制会把 A 服的
 * token 带到 B 服（撞身份），或者在提交配置时把 token 一起提交上去。
 *
 * ## 字段取舍
 *
 * - [identityId]：本机永久身份。**必须在凭据里**，否则重启后重新注册会变成「新服务器」，
 *   控制面上留下重复身份。
 * - [endpoint]：控制面地址。存下来是为了「重启后不必再配地址」——这正是零配置的目标。
 * - [token]：准入令牌。明文存盘是刻意取舍：agent 需无人值守自动重连，没有其它可信存储；
 *   文件权限收到 0600 作为补偿。
 * - [binding]：控制面确认的归属事实，断连时也可据此判断「上次绑到哪」。
 */
data class AgentCredentials(
    val identityId: String,
    val endpoint: String,
    val token: String,
    val binding: BoundIdentity? = null,
) {
    /** 是否具备「直接接入」的条件：地址与令牌都齐。 */
    val canConnect: Boolean get() = endpoint.isNotBlank() && token.isNotBlank()

    /** 已确认的绑定事实（由控制面权威下发，agent 不自行编造）。 */
    data class BoundIdentity(
        val namespace: String,
        val serverId: String,
        val kind: String,
        val address: String,
        val boundAt: Instant?,
    )
}

/**
 * 身份凭据的读写（JSON，原子写，权限尽力 0600）。
 *
 * ## 用 [JsonCodec] 而不是手写解析
 *
 * 手写 JSON 解析会为每个字段产生一个函数，既触发复杂度规则，也把「转义、嵌套、类型」
 * 这类本该由成熟库处理的细节变成自担风险。core 已有 [JsonCodec] 抽象（壳层注入实现），
 * 直接复用即可——core 依然不依赖具体库。
 *
 * ## 与旧 `identity.yml` 的关系
 *
 * 旧文件只存 identityId + createdAt，无法支撑零配置接入。这里提供 [migrateFromLegacy]
 * 做**一次性迁移**：把旧 identityId 原样搬进新凭据，保证身份不漂移；旧文件保留不删，
 * 便于回滚。
 */
class AgentCredentialsStore(
    private val dataDir: Path,
    private val codec: JsonCodec,
) {
    private val file: Path = dataDir.resolve(CREDENTIALS_FILE_NAME)

    /** 凭据文件路径（供日志与诊断提示用）。 */
    fun filePath(): Path = file

    /** 读取凭据；不存在或损坏返回 null（调用方据此决定是否走 join 流程）。 */
    fun load(): AgentCredentials? {
        if (!Files.exists(file)) {
            return null
        }
        val text = runCatching { Files.readString(file, StandardCharsets.UTF_8) }.getOrNull() ?: return null
        // 解析失败一律当「无凭据」：宁可让流程走 join，也不能把半截数据当成有效地址。
        return runCatching { decode(text) }.getOrNull()
    }

    /**
     * 写入凭据。
     *
     * 原子写（tmp + move）避免写一半断电留下半截 JSON；写后尽力收 0600
     * （Windows 不支持 Posix 权限，此时静默跳过——不因平台差异让 join 失败）。
     */
    fun save(credentials: AgentCredentials) {
        Files.createDirectories(dataDir)
        val tmp = dataDir.resolve("$CREDENTIALS_FILE_NAME.tmp")
        Files.writeString(tmp, codec.encode(encode(credentials)), StandardCharsets.UTF_8)
        try {
            Files.move(tmp, file, StandardCopyOption.ATOMIC_MOVE, StandardCopyOption.REPLACE_EXISTING)
        } catch (_: AtomicMoveNotSupportedException) {
            Files.move(tmp, file, StandardCopyOption.REPLACE_EXISTING)
        }
        restrictPermissions()
    }

    /** 更新绑定事实（控制面确认后调用），其余字段保持不变。 */
    fun updateBinding(binding: AgentCredentials.BoundIdentity): AgentCredentials? {
        val current = load() ?: return null
        return current.copy(binding = binding).also { save(it) }
    }

    /**
     * 从旧 `identity.yml` 迁移 identityId。
     *
     * 只在「新凭据不存在、旧文件合法」时搬一次：已部署升级后身份不变，
     * 控制面不会多出一个重复身份。
     */
    fun migrateFromLegacy(legacy: StoredAgentIdentity): AgentCredentials? {
        if (Files.exists(file) || !legacy.isValid) {
            return null
        }
        // 迁移只搬身份标识；地址与令牌留给 join 命令或 config.yml 补。
        return AgentCredentials(identityId = legacy.identityId, endpoint = "", token = "").also { save(it) }
    }

    /** 生成一个新的 identityId（仅当既无新凭据也无旧身份时使用）。 */
    fun newIdentityId(): String = UUID.randomUUID().toString().lowercase(Locale.ROOT)

    private fun restrictPermissions() {
        runCatching {
            Files.setPosixFilePermissions(
                file,
                setOf(PosixFilePermission.OWNER_READ, PosixFilePermission.OWNER_WRITE),
            )
        }
    }

    /** 编码为可交给 codec 的泛型树（Map/List/基本类型）。 */
    private fun encode(c: AgentCredentials): Map<String, Any?> =
        linkedMapOf(
            "formatVersion" to CREDENTIALS_FORMAT_VERSION,
            "identityId" to c.identityId,
            "endpoint" to c.endpoint,
            "token" to c.token,
            "binding" to
                c.binding?.let { b ->
                    linkedMapOf(
                        "namespace" to b.namespace,
                        "serverId" to b.serverId,
                        "kind" to b.kind,
                        "address" to b.address,
                        "boundAt" to b.boundAt?.toString(),
                    )
                },
        )

    /**
     * 从泛型树还原凭据。
     *
     * 任何一处类型不符即整体判无效（抛异常由 [load] 兜住）：凭据是接入的根，
     * 半截可用比完全不可用更危险——它会让人以为「地址是对的」而实际是旧值。
     */
    @Suppress("UNCHECKED_CAST")
    private fun decode(text: String): AgentCredentials {
        val root = codec.decode(text) as? Map<String, Any?> ?: error("凭据不是 JSON 对象")
        val version = (root["formatVersion"] as? Number)?.toInt()
        require(version == CREDENTIALS_FORMAT_VERSION) { "凭据格式版本不支持：$version" }
        val identityId =
            (root["identityId"] as? String)?.takeIf { isUuidV4(it) }
                ?: error("身份标识不是合法 UUIDv4")
        val bindingMap = root["binding"] as? Map<String, Any?>
        val binding =
            bindingMap?.let { b ->
                val ns = b["namespace"] as? String ?: ""
                val sid = b["serverId"] as? String ?: ""
                if (ns.isBlank() || sid.isBlank()) {
                    null
                } else {
                    AgentCredentials.BoundIdentity(
                        namespace = ns,
                        serverId = sid,
                        kind = b["kind"] as? String ?: "",
                        address = b["address"] as? String ?: "",
                        boundAt = (b["boundAt"] as? String)?.let { runCatching { Instant.parse(it) }.getOrNull() },
                    )
                }
            }
        return AgentCredentials(
            identityId = identityId,
            endpoint = root["endpoint"] as? String ?: "",
            token = root["token"] as? String ?: "",
            binding = binding,
        )
    }

    private fun isUuidV4(raw: String): Boolean =
        try {
            UUID.fromString(raw).version() == 4
        } catch (_: IllegalArgumentException) {
            false
        }
}
