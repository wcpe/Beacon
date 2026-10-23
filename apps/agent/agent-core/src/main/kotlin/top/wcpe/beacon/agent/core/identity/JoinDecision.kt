package top.wcpe.beacon.agent.core.identity

/**
 * `/beacon join` 的决策逻辑（FR-276），与平台壳无关。
 *
 * ## 为什么把它从壳层抽出来
 *
 * 该逻辑原先写在 Bukkit 与 Bungee 两个 `BeaconJoinCommand` 里，两份除 package 行外
 * **逐字相同**——既要求双端同步维护，又因为埋在 TabooLib 的命令闭包里（private 方法 +
 * `ProxyCommandSender` 参数）而**一行测试都写不了**。
 *
 * FR-276 的验收口径明确要求「在已绑定实例上再次执行 `/beacon join` 被明确拒绝」且
 * 「必须有测试钉住」。没有可测接缝时这条要求只能落空，所以把决策收敛成纯函数：
 * 输入是「已有凭据 + 命令行参数」，输出是「该怎么办」，副作用（写盘、回显、触发装配）
 * 留在壳层。这样双端共用同一份实现，也用一份测试覆盖两端。
 *
 * ## 安全语义
 *
 * 已绑定（`binding != null`）时**一律拒绝**，且**不得回写任何字段**：
 * 允许 join 改指向会让控制面上一台服对应两个身份，或把已生效的归属悄悄换掉。
 * 要换控制面必须先停插件、由管理员解绑。
 */
sealed interface JoinDecision {
    /** 参数不足：回用法，不做任何改动。 */
    data object MissingArgs : JoinDecision

    /** 地址形态不合法（必须是 http/https 开头）：不做任何改动。 */
    data class InvalidEndpoint(val endpoint: String) : JoinDecision

    /**
     * 本机已绑定到某个 namespace/serverId，拒绝改指向。
     *
     * @param namespace 当前绑定的 namespace 编码（用于回显）
     * @param serverId 当前绑定的服务器标识（用于回显）
     */
    data class AlreadyBound(val namespace: String, val serverId: String) : JoinDecision

    /** 允许接入：返回**待写入**的凭据（壳层负责落盘与触发装配）。 */
    data class Proceed(val credentials: AgentCredentials) : JoinDecision

    companion object {
        /** 命令行期望的最小参数个数：地址 + 令牌。 */
        const val REQUIRED_ARG_COUNT = 2

        /**
         * 计算本次 join 的决策。
         *
         * @param existing 当前已落盘的凭据；首次接入时为 null
         * @param args 命令参数（**不含** `join` 子命令本身），已按空白切分
         * @param newIdentityId 生成新身份标识；仅在既无凭据也无有效身份时调用
         */
        fun decide(
            existing: AgentCredentials?,
            args: List<String>,
            newIdentityId: () -> String,
        ): JoinDecision =
            // 按「参数 → 归属 → 地址」三段依次判定，收敛成一个表达式而不是多出口 return：
            // 判定顺序本身就是语义（见下），写成单表达式能一眼看出优先级不会被中途插入的分支打乱。
            when {
                args.size < REQUIRED_ARG_COUNT -> MissingArgs

                // 已绑定即拒绝，且**先于地址校验**。
                //
                // 顺序是有意的：已绑定的机器上执行 join 本身就是误操作，此时若先报「地址格式不对」，
                // 运维会以为改对地址就能重接，改完再撞一次墙才看到真实原因。先报归属能让人一步到位
                // 去解绑，而不是在地址上白折腾一轮。
                existing?.binding != null ->
                    AlreadyBound(existing.binding!!.namespace, existing.binding!!.serverId)

                !looksLikeEndpoint(args[0]) -> InvalidEndpoint(args[0])

                else ->
                    Proceed(
                        AgentCredentials(
                            identityId = existing?.identityId?.takeIf { it.isNotBlank() } ?: newIdentityId(),
                            endpoint = args[0],
                            token = args[1],
                            // 走到这里 binding 必为 null；显式带上是为了将来加分支时不丢字段。
                            binding = null,
                        ),
                    )
            }

        /** 粗校验地址形态：必须是 http(s):// 开头。不追求完整 URL 校验（避免误拒内网写法）。 */
        fun looksLikeEndpoint(raw: String): Boolean =
            raw.startsWith("http://", ignoreCase = true) || raw.startsWith("https://", ignoreCase = true)

        /** 用法提示（双端回显同一句，避免两处文案漂移）。 */
        const val USAGE = "用法：/beacon join <控制面地址> <令牌>（例：/beacon join http://127.0.0.1:20020 bn_xxxxxxxx）"

        /** 已绑定时的拒绝文案。 */
        fun alreadyBoundMessage(
            namespace: String,
            serverId: String,
        ): String = "本机已绑定到 $namespace/$serverId，不通过 join 改指向。如需换控制面，请先停用插件并联系管理员解绑。"

        /** 地址格式错误的提示文案。 */
        fun invalidEndpointMessage(): String = "地址格式不对：应形如 http://127.0.0.1:20020 或 https://beacon.example.com"
    }
}
