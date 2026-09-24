package top.wcpe.beacon.agent.core.client

import top.wcpe.beacon.agent.core.transport.HttpResponse

// 数据面挂载的响应映射（FR-233）：从 BeaconApiRegister.kt 拆出，
// 使该文件的函数数不再触碰 detekt 的 TooManyFunctions 阈值，同时让「响应契约」自成一处。

/**
 * 把数据面挂载的最终响应映射为注册结果（FR-233 拆出，控制 registerLegacy 的圈复杂度）。
 *
 * 200 必须是「本控制面确认挂载成功」的应答；instanceKey 是注册成功的必要事实
 * （服务端恒回 namespace/serverId 拼成的实例键）。为空说明响应体根本不是本端点的业务应答
 * （如被中间层 / SPA 兜底以 200 接管），此时绝不能当作成功——否则插件会带着「已注册」的
 * 假象进入数据面，而控制面侧从未收到该实例。
 */
internal fun BeaconApiClient.mapDataPlaneResponse(resp: HttpResponse): RegisterOutcome {
    return try {
        when (resp.statusCode) {
            200 -> {
                val result = parseRegister(resp.body)
                if (result.instanceKey.isBlank()) {
                    RegisterOutcome.Failed("注册响应缺少 instanceKey（非本控制面应答）")
                } else {
                    RegisterOutcome.Success(result)
                }
            }

            409 -> RegisterOutcome.DuplicateServerId
            // 403：实例被控制面主动下线，拒绝接入（FR-49），区别于 409 重复 / 404 未注册。
            403 -> RegisterOutcome.OfflineRejected
            401 -> RegisterOutcome.Unauthorized
            400 -> RegisterOutcome.IdentityRequired
            else -> RegisterOutcome.Failed("非预期状态码 ${resp.statusCode}")
        }
    } catch (e: Exception) {
        // 响应体不是本控制面的 JSON（例如对端某个中间层返回了 HTML 错误页）时，
        // 解析会抛异常；此处兜底转为 Failed 交上层退避重试，绝不让解析异常逃逸打断注册循环。
        RegisterOutcome.Failed("注册响应解析失败（非 JSON）：${e.message ?: e::class.simpleName}")
    }
}
