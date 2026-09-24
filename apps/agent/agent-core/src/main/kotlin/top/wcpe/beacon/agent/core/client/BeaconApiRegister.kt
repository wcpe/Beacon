package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.transport.HttpRequest
import top.wcpe.beacon.agent.core.transport.HttpResponse

// ===== BeaconApiClient 扩展函数（从类内提取，不占类方法数） =====

/**
 * 新规范路径：数据面挂载（FR-233，见 ADR-0084）。
 *
 * 语义显式化为「挂载数据面」，与 v2 身份注册端点（POST /beacon/v2/agent/register）区分——
 * 两者同名 /register 曾是「该退役一个」这类误判的直接成因。
 */
private const val DATA_PLANE_ATTACH_PATH = "/beacon/v1/agent/data-plane/attach"

/** 旧兼容路径：控制面保留一个版本周期作兼容别名后移除，插件仅在 [DATA_PLANE_ATTACH_PATH] 返回 404 时回退使用。 */
private const val LEGACY_REGISTER_PATH = "/beacon/v1/agent/register"

/**
 * 注册：v2 身份注册（POST /beacon/v2/agent/register）active 后衔接数据面挂载
 * （POST /beacon/v1/agent/data-plane/attach）；无 v2 身份时直落数据面挂载。
 *
 * 数据面挂载的原名 /beacon/v1/agent/register 由控制面保留一个版本周期作**兼容别名**（FR-233 / ADR-0084），
 * 届时移除；插件只有在旧控制面（无新路由）返回 404 时才回退该旧路径重试一次。
 *
 * backends 为本机（仅 bc 代理）当前代理的后端子服 serverId 集合（FR-36 事实，非身份），
 * 由调用方按帧传入；仅当非空时才拼入报文（bukkit / 旧控制面下为空、不拼，向后兼容）。
 */
fun BeaconApiClient.register(
    identity: AgentIdentity,
    backends: List<String> = emptyList(),
): RegisterOutcome {
    if (identity.hasV2Identity()) {
        return registerV2(identity, backends)
    }
    return registerLegacy(identity, backends)
}

/** Bootstrap 阶段仅观察 v2 身份状态，绝不发起 legacy 数据面注册。 */
fun BeaconApiClient.bootstrapRegister(identity: AgentIdentity): RegisterOutcome {
    if (!identity.hasV2Identity()) return RegisterOutcome.IdentityRequired
    return registerV2Status(identity)
}

internal fun BeaconApiClient.registerLegacy(
    identity: AgentIdentity,
    backends: List<String> = emptyList(),
): RegisterOutcome {
    val body =
        buildMap {
            put("namespace", identity.namespace)
            put("serverId", identity.serverId)
            put("role", identity.role)
            put("groupHint", identity.groupHint)
            put("address", identity.address)
            put("version", identity.version)
            put("capacity", identity.capacity)
            put("weight", identity.weight)
            put("metadata", identity.metadata)
            // agent 自身构建版本：仅非空时附加，旧控制面 / 旧 agent 缺键即可（FR-86，见 ADR-0039）。
            if (identity.agentVersion.isNotBlank()) put("agentVersion", identity.agentVersion)
            // bc 后端归属事实：仅非空时附加，旧控制面忽略即可（FR-36）。
            if (backends.isNotEmpty()) put("backends", backends)
        }
    val payload = codec.encode(body)
    // 报文 / 头 / 超时完全一致，仅路径不同：新规范路径与兼容回退共用同一构造（避免两处漂移）。
    val postDataPlane: (String) -> HttpResponse? = { path ->
        exec(
            HttpRequest(
                method = "POST",
                url = "$base$path",
                headers = headers(withBody = true, identity = identity),
                body = payload,
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        )
    }

    // 先请求新规范路径；仅当 404（对端为尚未支持新路径的旧控制面）时才改走旧兼容路径重试一次，
    // 避免「插件先升级、控制面后升级」时注册中断（MC 插件与控制面分批部署很常见）。
    // 其余状态码（200 / 400 / 401 / 403 / 409）均由真实业务语义产生，回退会掩盖真实错误，
    // 例如 409 重复 serverId 会被误当作「版本不匹配」而重试（FR-233 §3.2）。
    val resp = postDataPlane(DATA_PLANE_ATTACH_PATH)
    val finalResp =
        if (resp?.statusCode == 404) {
            postDataPlane(LEGACY_REGISTER_PATH)
        } else {
            resp
        } ?: return RegisterOutcome.Failed(connectFailReason())

    return when (finalResp.statusCode) {
        200 -> RegisterOutcome.Success(parseRegister(finalResp.body))
        409 -> RegisterOutcome.DuplicateServerId
        // 403：实例被控制面主动下线，拒绝接入（FR-49），区别于 409 重复 / 404 未注册。
        403 -> RegisterOutcome.OfflineRejected
        401 -> RegisterOutcome.Unauthorized
        400 -> RegisterOutcome.IdentityRequired
        else -> RegisterOutcome.Failed("非预期状态码 ${finalResp.statusCode}")
    }
}

/** v2 身份注册：先走确认状态机，active 后再衔接 legacy 数据面注册。 */
internal fun BeaconApiClient.registerV2(
    identity: AgentIdentity,
    backends: List<String>,
): RegisterOutcome {
    return when (val outcome = registerV2Status(identity)) {
        is RegisterOutcome.ActiveBindingConfirmed -> {
            identity.bind(outcome.binding.namespace, outcome.binding.serverId, outcome.binding.compatAddress)
            registerLegacy(identity, backends)
        }
        else -> outcome
    }
}

internal fun BeaconApiClient.registerV2Status(identity: AgentIdentity): RegisterOutcome {
    val body =
        buildMap {
            put("identityId", identity.identityId)
            put("kind", v2Kind(identity.role))
            put("bootId", identity.bootId)
            // serverId 仅由控制面分配；旧本地键只留作 agent 侧迁移提示，绝不上传。
            if (identity.agentVersion.isNotBlank()) put("agentVersion", identity.agentVersion)
            // 服务器工作目录（FR-226）：仅非空时附加，旧控制面 / 旧 agent 缺键即可。
            if (identity.serverWorkDir.isNotBlank()) put("serverWorkDir", identity.serverWorkDir)
            appendEndpointReport(this, identity)
        }
    val resp =
        exec(
            HttpRequest(
                method = "POST",
                url = "$base/beacon/v2/agent/register",
                headers = headers(withBody = true),
                body = codec.encode(body),
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return RegisterOutcome.Failed(connectFailReason())

    return when (resp.statusCode) {
        200 -> activeRegisterOutcome(resp.body)
        202 -> pendingApprovalOutcome(resp.body, identity)
        401 -> RegisterOutcome.Unauthorized
        403 -> RegisterOutcome.Rejected
        409 -> RegisterOutcome.IdentityConflict
        400 -> RegisterOutcome.IdentityRequired
        else -> RegisterOutcome.Failed("非预期状态码 ${resp.statusCode}")
    }
}

internal fun BeaconApiClient.activeRegisterOutcome(body: String): RegisterOutcome {
    return when (parseRegistrationStatus(body)) {
        "active" -> {
            val binding = parseActiveBinding(body) ?: return RegisterOutcome.Failed("控制面未返回权威绑定")
            RegisterOutcome.ActiveBindingConfirmed(binding)
        }
        "disabled" -> RegisterOutcome.Disabled
        "conflict" -> RegisterOutcome.IdentityConflict
        "unbound" -> RegisterOutcome.Unbound
        else -> RegisterOutcome.Failed("非预期注册状态")
    }
}

internal fun BeaconApiClient.pendingApprovalOutcome(
    body: String,
    identity: AgentIdentity,
): RegisterOutcome.PendingApproval {
    val obj = JsonTree.asObject(codec.decode(body))
    return RegisterOutcome.PendingApproval(
        serverId = JsonTree.strOr(obj, "serverId", ""),
        namespace = JsonTree.strOr(obj, "namespace", identity.namespace),
    )
}

/** v2 注册确认状态长轮询。 */
fun BeaconApiClient.pollRegistration(
    identity: AgentIdentity,
    waitSeconds: Int,
): RegistrationPollResult {
    if (!identity.hasV2Identity()) return RegistrationPollResult.Failed("缺少 v2 身份")
    val resp =
        exec(
            HttpRequest(
                method = "GET",
                url = "$base/beacon/v2/agent/registration?wait=$waitSeconds",
                headers = v2IdentityHeaders(identity),
                body = null,
                readTimeoutMs = waitSeconds * 1000L + settings.requestTimeoutMs,
            ),
        ) ?: return RegistrationPollResult.Failed(connectFailReason())
    return when (resp.statusCode) {
        200 ->
            when (parseRegistrationStatus(resp.body)) {
                "active" -> {
                    val binding =
                        parseActiveBinding(resp.body)
                            ?: return RegistrationPollResult.Failed("控制面未返回权威绑定")
                    RegistrationPollResult.Active(binding)
                }
                "pending" -> RegistrationPollResult.Pending
                "disabled" -> RegistrationPollResult.Disabled
                "rejected" -> RegistrationPollResult.Rejected
                "conflict" -> RegistrationPollResult.Conflict
                "unbound" -> RegistrationPollResult.Unbound
                else -> RegistrationPollResult.Failed("非预期注册状态")
            }

        304 -> RegistrationPollResult.NotModified
        else -> RegistrationPollResult.Failed("非预期状态码 ${resp.statusCode}")
    }
}

internal fun BeaconApiClient.parseRegister(jsonBody: String): RegisterResult {
    val obj = JsonTree.asObject(codec.decode(jsonBody))
    return RegisterResult(
        instanceKey = JsonTree.strOr(obj, "instanceKey", ""),
        resolvedGroup = JsonTree.str(obj, "resolvedGroup"),
        resolvedZone = JsonTree.str(obj, "resolvedZone"),
        heartbeatIntervalSec = JsonTree.intOr(obj, "heartbeatIntervalSec", 0),
        ttlSec = JsonTree.intOr(obj, "ttlSec", 0),
        assigned = JsonTree.boolOr(obj, "assigned", false),
    )
}

internal fun BeaconApiClient.parseRegistrationStatus(jsonBody: String): String {
    val obj = JsonTree.asObject(codec.decode(jsonBody))
    return JsonTree.strOr(obj, "status", "")
}
