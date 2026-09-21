package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.identity.AgentIdentity
import top.wcpe.beacon.agent.core.transport.HttpRequest

// ===== BeaconApiClient 扩展函数（从类内提取，不占类方法数） =====

/**
 * 注册：POST /beacon/v1/agent/register。
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
    val resp =
        exec(
            HttpRequest(
                method = "POST",
                url = "$base/beacon/v1/agent/register",
                headers = headers(withBody = true, identity = identity),
                body = codec.encode(body),
                readTimeoutMs = settings.requestTimeoutMs,
            ),
        ) ?: return RegisterOutcome.Failed(connectFailReason())

    return when (resp.statusCode) {
        200 -> RegisterOutcome.Success(parseRegister(resp.body))
        409 -> RegisterOutcome.DuplicateServerId
        // 403：实例被控制面主动下线，拒绝接入（FR-49），区别于 409 重复 / 404 未注册。
        403 -> RegisterOutcome.OfflineRejected
        401 -> RegisterOutcome.Unauthorized
        400 -> RegisterOutcome.IdentityRequired
        else -> RegisterOutcome.Failed("非预期状态码 ${resp.statusCode}")
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
