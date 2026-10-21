package top.wcpe.beacon.agent.bungee

import taboolib.common.platform.ProxyCommandSender
import taboolib.common.platform.command.command
import top.wcpe.beacon.agent.core.client.sendMessage
import top.wcpe.beacon.agent.core.identity.AgentCredentials
import top.wcpe.beacon.agent.core.identity.AgentCredentialsStore

/**
 * `/beacon join <控制面地址> <令牌>`：零配置接入的入口（FR-276）。
 *
 * ## 为什么这个命令必须独立于 `/beacon` 的其余子命令
 *
 * 其余子命令（status/reload/reconnect/resync）都依赖「已装配的运行时」，
 * 而 join 是**装配之前**的动作——它要在没有 lifecycle 的时候就能执行。
 * 故两处分批注册：本类在 `enable()` 里先注册（接入前），
 * 运维子命令仍在接入成功后注册（那里才有 lifecycle）。
 *
 * ## 它只做一件事：写凭据 + 触发接入
 *
 * 不直接建连接——连接由 [onJoined] 交给壳层重跑装配流程。
 * 这样「凭据怎么存」与「怎么连」保持解耦，Bungee 壳可以复用同一套语义。
 *
 * ## 安全边界
 *
 * 命令需要 `beacon.admin` 权限（与 `/beacon` 其余子命令一致）。令牌会写到
 * `agent-credentials.json`（权限尽力收 0600），**不会**写进 config.yml——
 * config.yml 会被纳入版本管理、被模板复制分发，凭据混进去等于扩散泄露面。
 *
 * @param credentials 凭据存储（与启动流程共用同一实例，避免两处读到不同状态）
 * @param onJoined 凭据写好后触发接入的回调（壳层传入可重入的装配入口）
 * @param info 正常信息回显
 * @param warning 异常提示回显
 */
object BeaconJoinCommand {
    /** 注册 join 命令；在 `enable()` 中调用，早于任何接入尝试。 */
    fun register(
        credentials: AgentCredentialsStore,
        onJoined: () -> Unit,
        logger: JoinLogger,
    ) {
        command("beacon", permission = "beacon.admin") {
            literal("join", description = "接入 Beacon：/beacon join <控制面地址> <令牌>") {
                execute<ProxyCommandSender> { sender, context, _ ->
                    val raw = context.self().trim()
                    val parts = raw.split(WHITESPACE).filter { it.isNotEmpty() }
                    // TabooLib 的上下文在这里只给整串，故自行切分参数。
                    // 期望形态：join <地址> <令牌>
                    val args = parts.drop(1)
                    if (args.size < 2) {
                        sender.sendMessage(USAGE)
                        return@execute
                    }
                    val request = JoinRequest(args[0], args[1])
                    if (!looksLikeEndpoint(request.endpoint)) {
                        sender.sendMessage("地址格式不对：应形如 http://127.0.0.1:20020 或 https://beacon.example.com")
                        return@execute
                    }
                    join(sender, credentials, request, onJoined, logger)
                }
            }
            // 无参执行 join 时回用法（而不是静默）。
            literal("join", description = "接入 Beacon") {
                execute<ProxyCommandSender> { sender, _, _ ->
                    sender.sendMessage(USAGE)
                }
            }
        }
    }

    /** 执行接入：写凭据 → 提示 → 触发装配。 */
    private fun join(
        sender: ProxyCommandSender,
        credentials: AgentCredentialsStore,
        request: JoinRequest,
        onJoined: () -> Unit,
        logger: JoinLogger,
    ) {
        val existing = credentials.load()
        // 已绑定的实例不允许用 join 改指向：那会让控制面上一台服对应两个身份，
        // 或把已生效的归属悄悄换掉。要换控制面请显式关掉插件后手工处理。
        if (existing?.binding != null) {
            sender.sendMessage(
                "本机已绑定到 ${existing.binding!!.namespace}/${existing.binding!!.serverId}，" +
                    "不通过 join 改指向。如需换控制面，请先停用插件并联系管理员解绑。",
            )
            return
        }
        val identityId = existing?.identityId?.takeIf { it.isNotBlank() } ?: credentials.newIdentityId()
        // 保留已有绑定（此处必为 null，但显式带上避免将来改逻辑时丢字段）。
        val saved =
            AgentCredentials(
                identityId = identityId,
                endpoint = request.endpoint,
                token = request.token,
                binding = existing?.binding,
            )
        runCatching { credentials.save(saved) }
            .onFailure { e ->
                // 写盘失败必须让人看见：不写盘的 join 等于什么都没做，却容易被误认为成功。
                val msg = "写入凭据失败：${e.javaClass.simpleName}: ${e.message}"
                logger.warning(msg)
                sender.sendMessage(msg)
                return
            }
        val where = credentials.filePath().toString()
        logger.info("已保存接入凭据：$where")
        sender.sendMessage("已保存接入凭据，正在连接 ${request.endpoint} …")
        sender.sendMessage("若控制面要求审批，请联系管理员在 Beacon 后台确认。")
        // 触发装配（可重入）：凭据已落盘，装配流程会读到它。
        runCatching { onJoined() }.onFailure { e ->
            val msg = "触发接入失败：${e.javaClass.simpleName}: ${e.message}"
            logger.warning(msg)
            sender.sendMessage(msg)
        }
    }

    /** 粗校验地址形态：必须是 http(s):// 开头。不追求完整 URL 校验（避免误拒内网写法）。 */
    private fun looksLikeEndpoint(raw: String): Boolean =
        raw.startsWith("http://", ignoreCase = true) || raw.startsWith("https://", ignoreCase = true)

    private const val USAGE = "用法：/beacon join <控制面地址> <令牌>（例：/beacon join http://127.0.0.1:20020 bn_xxxxxxxx）"

    private val WHITESPACE = Regex("\\s+")

    /** 一次 join 请求的参数（地址 + 令牌）。 */
    data class JoinRequest(val endpoint: String, val token: String)

    /** 日志回调组：info/warning 收成一个参数，避免 join 参数超阈值。 */
    data class JoinLogger(
        val info: (String) -> Unit,
        val warning: (String) -> Unit,
    )
}
