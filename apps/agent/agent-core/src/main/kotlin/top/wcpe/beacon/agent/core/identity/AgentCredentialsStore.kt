package top.wcpe.beacon.agent.core.identity

import top.wcpe.beacon.agent.core.client.ActiveBinding
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.nio.channels.Channels
import java.nio.channels.SeekableByteChannel
import java.nio.charset.StandardCharsets
import java.nio.file.AtomicMoveNotSupportedException
import java.nio.file.Files
import java.nio.file.Path
import java.nio.file.StandardCopyOption
import java.nio.file.StandardOpenOption
import java.nio.file.attribute.PosixFilePermission
import java.nio.file.attribute.PosixFilePermissions
import java.time.Instant
import java.time.ZoneOffset
import java.time.format.DateTimeFormatter
import java.util.Locale
import java.util.UUID
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.locks.ReentrantLock
import kotlin.concurrent.withLock

/** 身份凭据文件名。与 config.yml 分离，避免「凭据混进配置」被误提交或误读。 */
private const val CREDENTIALS_FILE_NAME = "agent-credentials.json"

/** 凭据格式版本：后续字段演进靠它识别，读到未知版本即拒绝而不是猜。 */
private const val CREDENTIALS_FORMAT_VERSION = 1

/**
 * 损坏凭据的备份名时间戳格式：UTC、去掉 `:`（Windows 文件名不接受）、毫秒级避免同名覆盖。
 */
private val CORRUPT_STAMP_FORMAT: DateTimeFormatter =
    DateTimeFormatter.ofPattern("yyyyMMdd-HHmmss-SSS").withZone(ZoneOffset.UTC)

/**
 * 一次凭据读取的**状态**（区别于 [AgentCredentials] 的值）。
 *
 * 拆出状态是为了让「能不能覆盖」这件事有依据：只有 [Absent] 才能安全写入，
 * [Unreadable] 必须备份后再说，[UnsupportedVersion] 一律不许动。
 */
internal sealed interface CredentialsRead {
    /** 文件不存在：这是唯一可以放心写入的状态。 */
    object Absent : CredentialsRead

    /** 文件在，但读不出来（JSON 损坏、字段非法、无法读取）：里面的地址/令牌可能还能人工抢救。 */
    data class Unreadable(
        val reason: String,
    ) : CredentialsRead

    /** 文件在，但由更高版本的 agent 写入：拒绝任何覆盖，降级不能变成数据销毁。 */
    data class UnsupportedVersion(
        val version: Int?,
    ) : CredentialsRead

    /** 正常读到一份可用凭据。 */
    data class Ok(
        val credentials: AgentCredentials,
    ) : CredentialsRead
}

/**
 * 凭据文件的权限集：只有属主可读写。
 *
 * 凭据里有准入令牌，同机其他用户能读到就等于令牌泄漏；故写盘时**创建即 0600**，
 * 写完再 chmod 收一次作为兜底。
 */
private val OWNER_ONLY: Set<PosixFilePermission> = setOf(PosixFilePermission.OWNER_READ, PosixFilePermission.OWNER_WRITE)

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
 * ## 身份的唯一真源
 *
 * 历史包袱是「两个地方各存一份 identityId」：早期 `identity.yml` 与后来凭据文件互不同步，
 * 于是 `/beacon join` 写进凭据的身份从来没人读，运行期只认 `identity.yml`。
 * [resolveIdentityId] 把这件事收敛成一处判断（凭据 > 旧文件迁移 > 新生成），
 * 壳层不再自己决定用哪份身份。
 *
 * ## 与旧 `identity.yml` 的关系
 *
 * 旧文件只存 identityId + createdAt，无法支撑零配置接入。这里提供 [migrateFromLegacy]
 * 做**一次性迁移**：把旧 identityId 原样搬进新凭据，保证身份不漂移；旧文件保留不删，
 * 便于回滚。
 *
 * @param dataDir agent 数据目录
 * @param codec JSON 编解码（壳层注入）
 * @param warn 告警出口。迁移、权限收紧、凭据文件不可读这类降级都要显式告知运维，不能静默
 * @param error 错误出口（默认升级到 [warn]）。只用于「必须拒绝、且动了文件就等于毁数据」的事件：
 *              读到更高版本写入的凭据、以及备份坏文件失败导致迁移被放弃
 */
class AgentCredentialsStore(
    private val dataDir: Path,
    private val codec: JsonCodec,
    private val warn: (String) -> Unit = {},
    private val error: (String) -> Unit = warn,
) {
    private val file: Path = dataDir.resolve(CREDENTIALS_FILE_NAME)

    /** 串行化「读 → 改 → 写」：join、启动解析、绑定落盘可能来自不同线程，交错会丢字段。 */
    private val writeLock = ReentrantLock()

    /** 权限降级只告警一次：每次写盘都刷会让真正的故障沉在噪音里。 */
    private val degradeWarned = AtomicBoolean(false)

    /** 凭据文件路径（供日志与诊断提示用）。 */
    fun filePath(): Path = file

    /** 读取凭据；不存在或损坏返回 null（调用方据此决定是否走 join 流程）。 */
    fun load(): AgentCredentials? = (loadResult() as? CredentialsRead.Ok)?.credentials

    /**
     * 读取凭据，并把结果分成**四类状态**：[CredentialsRead.Absent] / [CredentialsRead.Unreadable] /
     * [CredentialsRead.UnsupportedVersion] / [CredentialsRead.Ok]。
     *
     * ## 为什么不能只有「读到 / 没读到」
     *
     * [load] 把「文件不存在」和「文件在、但读不出来」归一成 null。对「要不要 join」这个判断没问题，
     * 但**写入方**（迁移、生成新身份）据此就会以为「可以放心覆盖」——于是：
     *  - 身份被旧文件里的 ID 静默换掉（控制面多一个孤儿身份，原「已绑定」事实失联）；
     *  - 原 endpoint / token 被原子 move 彻底替换，agent 再也回不到已连接状态；
     *  - 全程无备份、无告警。
     *
     * 故读写判定必须分开：只有 [CredentialsRead.Absent] 才是「可以写」的安全前提。
     */
    internal fun loadResult(): CredentialsRead = readCredentialsFile(codec, file)

    /**
     * 解析本机身份标识（单一真源）。
     *
     * 优先级：
     * 1. 凭据文件里的合法 identityId —— 直接用它，**不覆盖**，也不再参考旧文件是否损坏；
     * 2. 旧 `identity.yml` 合法 —— 调用 [migrateFromLegacy] 迁移一次，用迁移后的身份；
     * 3. 两者都没有 —— 生成新身份并立即落盘（不落盘则每次重启都变成一台「新服务器」）。
     *
     * 迁移或落盘失败都不会阻断本次接入（旧文件仍在，下次启动重试），但一定会告警。
     *
     * **凭据文件存在却读不出可用凭据时，绝不写它**：那份文件里可能还有能人工抢救的
     * endpoint / token，一旦被新身份覆盖，agent 就再也回不到已连接/已绑定状态。
     * 此时按旧文件（若有）沿用身份，否则直接返回一个不落盘的新身份。
     *
     * @param legacy 旧 `identity.yml` 的读取结果（可能合法、可能损坏、可能不存在）
     * @return 最终使用的 identityId
     */
    fun resolveIdentityId(legacy: StoredAgentIdentity): String =
        when (val read = loadResult()) {
            // 1) 凭据是单一真源：读到合法身份就直接用，旧 identity.yml 损坏与否都不影响。
            is CredentialsRead.Ok -> read.credentials.identityId
            // 2) 文件在、却用不了：保持原文件原样（见本函数注释），显式呈现后沿用旧身份。
            is CredentialsRead.Unreadable, is CredentialsRead.UnsupportedVersion -> {
                reportUnusable(read, file, warn, error)
                if (legacy.isValid) legacy.identityId else newIdentityId()
            }
            // 3) 旧文件合法 → 迁移一次（写盘失败已在 migrateFromLegacy 内告警，此时沿用旧身份，
            //    旧文件没删所以下次启动会重试，绝不换成一个新身份）；
            // 4) 两者都没有 → 造新身份并落盘，失败同样不静默。
            is CredentialsRead.Absent ->
                if (legacy.isValid) {
                    migrateFromLegacy(legacy)?.identityId ?: legacy.identityId
                } else {
                    newIdentityId().also { fresh ->
                        runCatching { save(AgentCredentials(identityId = fresh, endpoint = "", token = "")) }
                            .onFailure { warn("生成新身份后写入凭据失败（$file）：${describe(it)}，本次仍用该身份接入") }
                    }
                }
        }

    /**
     * 写入凭据。
     *
     * 原子写（tmp + move）避免写一半断电留下半截 JSON；tmp 名带随机后缀，并发写各持独立文件，
     * 不会互相截断。权限在**创建那一刻**即为 0600（见 [openOwnerOnly]）。
     */
    fun save(credentials: AgentCredentials) {
        writeLock.withLock { writeAtomically(codec.encode(encodeCredentials(credentials))) }
    }

    /**
     * 更新绑定事实（控制面确认归属后调用），其余字段保持不变。
     *
     * 与已存绑定完全一致时不写盘：绑定每次接入确认都会到来，无谓刷盘只会让文件时间戳抖动。
     * 读 → 改 → 写整体持锁，避免与 join 的写入交错丢字段。
     *
     * @return 更新后的凭据；无凭据时返回 null（此时无从更新，也说明流程还没 join 过）
     */
    fun updateBinding(binding: AgentCredentials.BoundIdentity): AgentCredentials? =
        writeLock.withLock {
            load()?.let { current ->
                if (current.binding == binding) current else current.copy(binding = binding).also { save(it) }
            }
        }

    /**
     * 从旧 `identity.yml` 迁移 identityId。
     *
     * 只在「还没有可用凭据、旧文件合法」时搬一次：已部署升级后身份不变，
     * 控制面不会多出一个重复身份。按凭据文件的**状态**分三种处理：
     *  - 不存在 → 正常迁移；
     *  - 存在但读不出来 → **先把坏文件改名成 `.corrupt-<时间戳>` 备份**，再迁移并告警。
     *    那些文件里可能还留着可用的 endpoint / token，先备份才有人工抢救的机会；
     *  - 由更高版本写入 → **拒绝迁移**并打 ERROR。降级路径绝不能变成数据销毁：
     *    旧 agent 读不懂新版字段，覆盖只会让新版信息永久消失。
     *
     * @return 迁移得到的凭据；已有可用凭据、旧文件不合法、或迁移被拒绝时返回 null
     */
    fun migrateFromLegacy(legacy: StoredAgentIdentity): AgentCredentials? =
        writeLock.withLock {
            val read = loadResult()
            when {
                // 旧文件本身不合法（没有可搬的身份），或已有可用凭据（不得覆盖）→ 什么都不做。
                !legacy.isValid || read is CredentialsRead.Ok -> null
                // 更高版本：一动不动，打 ERROR。文件里是新版 agent 写的东西，旧 agent 没有资格覆盖。
                read is CredentialsRead.UnsupportedVersion -> {
                    error(
                        "凭据文件由更高版本的 agent 写入（formatVersion=${read.version}，本 agent 支持 " +
                            "$CREDENTIALS_FORMAT_VERSION）：已拒绝迁移以免覆盖新版数据，$file 保持原样。" +
                            "本次沿用旧身份接入；要恢复凭据请升级 agent 或人工处理该文件",
                    )
                    null
                }
                // 存在但读不出来：先备份再迁移，且把备份路径告诉运维（这是人工抢救的唯一入口）。
                read is CredentialsRead.Unreadable ->
                    backUpUnreadable(file)?.let { backup ->
                        warn(
                            "凭据文件不可读（${read.reason}），已备份为 ${backup.fileName} 后按旧身份迁移；" +
                                "若原文件里有可用的地址/令牌，可从该备份人工恢复",
                        )
                        persistMigrated(legacy, file, ::save, warn)
                    } ?: run {
                        // 备份失败就不再往下走：直接覆盖等于把「人工抢救」的最后一份材料也销毁。
                        error(
                            "凭据文件不可读（${read.reason}）且备份失败：已放弃本次迁移，$file 保持原样。" +
                                "请检查数据目录写权限后重启",
                        )
                        null
                    }
                // 文件不存在：这是唯一可以放心写入的状态。
                else -> persistMigrated(legacy, file, ::save, warn)
            }
        }

    /** 生成一个新的 identityId（仅当既无新凭据也无旧身份时使用）。 */
    fun newIdentityId(): String = UUID.randomUUID().toString().lowercase(Locale.ROOT)

    /** 原子写：随机 tmp → 0600 建文件 → 写入 → move 覆盖 → chmod 兜底 → 清理 tmp。 */
    private fun writeAtomically(text: String) {
        Files.createDirectories(dataDir)
        val tmp = dataDir.resolve("$CREDENTIALS_FILE_NAME.${UUID.randomUUID()}.tmp")
        try {
            // 走 OutputStream 写入：写满整个缓冲，不必自己循环处理 channel 的部分写。
            openOwnerOnly(tmp).use { channel ->
                Channels.newOutputStream(channel).write(text.toByteArray(StandardCharsets.UTF_8))
            }
            try {
                Files.move(tmp, file, StandardCopyOption.ATOMIC_MOVE, StandardCopyOption.REPLACE_EXISTING)
            } catch (_: AtomicMoveNotSupportedException) {
                Files.move(tmp, file, StandardCopyOption.REPLACE_EXISTING)
            }
            // chmod 兜底：创建时的 0600 已生效，这里再收一次，防 umask 或复制/中间件把它改宽。
            runCatching { Files.setPosixFilePermissions(file, OWNER_ONLY) }
                .onFailure { warnDegradeOnce("收紧凭据文件权限", it, degradeWarned, warn) }
        } finally {
            runCatching { Files.deleteIfExists(tmp) }
        }
    }

    /**
     * 以 0600 创建 tmp 文件并返回可写通道。
     *
     * 权限必须在**创建那一刻**就是 0600：先落盘再收紧，中间那段时间凭据（含令牌）对同机
     * 其他用户可读。文件系统不支持 Posix 权限（Windows 等）时降级为普通创建并告警一次。
     */
    private fun openOwnerOnly(tmp: Path): SeekableByteChannel =
        runCatching {
            Files.newByteChannel(
                tmp,
                setOf(StandardOpenOption.CREATE_NEW, StandardOpenOption.WRITE),
                PosixFilePermissions.asFileAttribute(OWNER_ONLY),
            )
        }.getOrElse { cause ->
            if (cause !is UnsupportedOperationException) {
                throw cause
            }
            warnDegradeOnce("按 0600 创建凭据文件", cause, degradeWarned, warn)
            Files.newByteChannel(tmp, setOf(StandardOpenOption.CREATE_NEW, StandardOpenOption.WRITE))
        }
}

/**
 * 把控制面确认的绑定事实转成凭据里持久化的形态。
 *
 * 放在 core 而不是各平台壳：两个壳要做**完全相同**的转换（含 boundAt 解析容错），
 * 写两份必然出现「一处改了另一处忘」——而这份 binding 正是 join「已绑定则拒绝」的依据。
 *
 * @param kind 壳的角色名（bukkit / bungee），与 [AgentIdentity.role] 同源
 */
fun ActiveBinding.toCredentialsBinding(kind: String): AgentCredentials.BoundIdentity =
    AgentCredentials.BoundIdentity(
        namespace = namespace,
        serverId = serverId,
        kind = kind,
        address = compatAddress,
        // boundAt 由控制面下发，格式异常不该让绑定整体失效——解析不出就当没有时间戳。
        boundAt = runCatching { Instant.parse(boundAt) }.getOrNull(),
    )

/** 编码为可交给 codec 的泛型树（Map/List/基本类型）。 */
private fun encodeCredentials(c: AgentCredentials): Map<String, Any?> =
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
 * 任何一处类型不符即整体判无效（由 [classifyCredentials] 折算成 [CredentialsRead.Unreadable]）：
 * 凭据是接入的根，半截可用比完全不可用更危险——它会让人以为「地址是对的」而实际是旧值。
 */
@Suppress("UNCHECKED_CAST")
private fun credentialsFrom(root: Map<String, Any?>): AgentCredentials {
    // 版本已在 [classifyCredentials] 里先行判定（高版本要单独识别）；这里再挡一道，
    // 保证「读不懂的版本」永远不会退化成「按字段猜着读」。
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

/**
 * 读凭据文件并分成四类状态。
 *
 * 「文件不存在」与「文件在但读不出来」必须分开：前者可以放心写，后者一写就把运维
 * 唯一的抢救材料覆盖掉了（详见 [AgentCredentialsStore.loadResult]）。
 */
@Suppress("UNCHECKED_CAST")
private fun readCredentialsFile(
    codec: JsonCodec,
    file: Path,
): CredentialsRead {
    // 只 stat 一次：exists 与「读不出文本」必须分开判，否则两者会被合并回同一个 null。
    val exists = Files.exists(file)
    val text = if (exists) runCatching { Files.readString(file, StandardCharsets.UTF_8) }.getOrNull() else null
    val root = text?.let { runCatching { codec.decode(it) }.getOrNull() as? Map<String, Any?> }
    val version = (root?.get("formatVersion") as? Number)?.toInt()
    return when {
        !exists -> CredentialsRead.Absent
        text == null -> CredentialsRead.Unreadable("文件不可读取")
        root == null -> CredentialsRead.Unreadable("内容不是合法 JSON 对象")
        // 缺 formatVersion 只可能是损坏（正常写入必带该字段），按「不可读」处理才有备份后迁移的机会；
        // 只有**明确写了一个不同版本**才是降级场景，必须拒绝覆盖。
        version == null -> CredentialsRead.Unreadable("缺少 formatVersion 字段")
        version != CREDENTIALS_FORMAT_VERSION -> CredentialsRead.UnsupportedVersion(version)
        else ->
            runCatching { credentialsFrom(root) }.fold(
                onSuccess = { CredentialsRead.Ok(it) },
                onFailure = { CredentialsRead.Unreadable(describe(it)) },
            )
    }
}

/** 权限收紧不可用时**告警一次**：静默降级会让运维以为凭据仍受保护。 */
private fun warnDegradeOnce(
    action: String,
    cause: Throwable,
    warned: AtomicBoolean,
    warn: (String) -> Unit,
) {
    if (warned.compareAndSet(false, true)) {
        warn("$action 失败（${describe(cause)}）：当前文件系统不支持 0600 权限，凭据将沿用系统默认权限，请自行限制数据目录访问")
    }
}

/** 凭据文件存在却用不了时的显式呈现：读不出来按 warning 降级，高版本按 ERROR 拒绝。 */
private fun reportUnusable(
    read: CredentialsRead,
    file: Path,
    warn: (String) -> Unit,
    error: (String) -> Unit,
) {
    when (read) {
        is CredentialsRead.UnsupportedVersion ->
            error(
                "凭据文件由更高版本的 agent 写入（formatVersion=${read.version}，本 agent 支持 " +
                    "$CREDENTIALS_FORMAT_VERSION）：未改动 $file，本次沿用旧身份接入。请升级 agent 后再启动，不要删除该文件",
            )
        is CredentialsRead.Unreadable ->
            warn("凭据文件不可读（${read.reason}）：未改动 $file，本次沿用旧身份接入；如需恢复地址/令牌请人工处理该文件")
        else -> Unit
    }
}

/**
 * 把不可读的凭据文件改名成 `agent-credentials.json.corrupt-<时间戳>`。
 *
 * 先备份再迁走，是因为原文件里可能还留着能用的 endpoint / token（也许只是某个字段被写坏）；
 * 直接原子覆盖会让「人工抢救」失去唯一材料。时间戳用 UTC 且不含 `:`（Windows 文件名非法）。
 *
 * @return 备份路径；备份失败返回 null（调用方据此放弃迁移，绝不覆盖原文件）
 */
private fun backUpUnreadable(file: Path): Path? {
    val backup = file.resolveSibling("${file.fileName}.corrupt-${CORRUPT_STAMP_FORMAT.format(Instant.now())}")
    return runCatching {
        Files.move(file, backup, StandardCopyOption.REPLACE_EXISTING)
        backup
    }.getOrNull()
}

/**
 * 迁移落盘：只搬身份标识，地址与令牌留给 join 命令或 config.yml 补。
 *
 * 写在类外（顶层私有）而不是成员：本类已贴着 detekt 的类内函数数上限，而这段逻辑
 * 不碰任何实例状态，只需要「存」与「告警」两个出口（与 `sanitizeForReceipt` 同一处理方式）。
 */
private fun persistMigrated(
    legacy: StoredAgentIdentity,
    file: Path,
    persist: (AgentCredentials) -> Unit,
    warn: (String) -> Unit,
): AgentCredentials {
    val migrated = AgentCredentials(identityId = legacy.identityId, endpoint = "", token = "")
    // 迁移没落盘等于没迁移：下次启动会重来，运维也看不到。失败必须显式告知。
    runCatching { persist(migrated) }
        .onFailure { warn("迁移旧身份到凭据失败（$file）：${describe(it)}，下次启动会重试") }
    return migrated
}

private fun isUuidV4(raw: String): Boolean =
    try {
        UUID.fromString(raw).version() == 4
    } catch (_: IllegalArgumentException) {
        false
    }

/** 拼出简洁可读的异常描述，避免日志里只留一句无信息量的类名。 */
private fun describe(cause: Throwable): String = "${cause.javaClass.simpleName}: ${cause.message}"
