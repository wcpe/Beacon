package top.wcpe.beacon.agent.core.messaging

/**
 * 可选名册实现的可变持有者（FR-31 / ADR-0022），本身即一个降级版 [RosterDirectory]。
 *
 * [DiscoveryView][top.wcpe.beacon.agent.core.api.DiscoveryView] 在装配期即创建（早于 messaging 模块启动），
 * 而真正的名册实现（Redis HGETALL）要等 Redis 配置下发、消息模块启动后才就绪。故装配期把本持有者注入
 * DiscoveryView，壳层在消息模块启动成功后 [set] 实际实现、停止 / 重连失败时 [reset] 复位。
 *
 * 优雅降级（与 [MessagingHolder] 同构）：未注入实现、或实现读取抛异常时，[snapshot] 返回空 Map、绝不外抛，
 * 业务插件据此走自身降级（守 ADR-0022 决策 5、不变量 #5 fail-static）。
 *
 * @param warn WARN 日志回调（实现读取异常时记一行，默认无操作）
 */
class RosterDirectoryHolder(
    private val warn: (String) -> Unit = {},
) : RosterDirectory {

    /** 当前注入的名册实现；null 表示未注入（messaging 未开 / Redis 未连）。 */
    @Volatile
    private var current: RosterDirectory? = null

    /** 切换为活跃名册实现（消息模块启动成功后由壳层注入）。 */
    fun set(directory: RosterDirectory) {
        current = directory
    }

    /** 复位为未注入（消息模块停止 / 重连失败时调用）。 */
    fun reset() {
        current = null
    }

    /**
     * 读取全量名册快照；未注入实现或读取异常时降级返回空 Map（绝不外抛）。
     */
    override fun snapshot(): Map<String, String> {
        val directory = current ?: return emptyMap()
        return try {
            directory.snapshot()
        } catch (t: Throwable) {
            // 名册读取异常（如 Redis 断连）：降级返空，业务插件据此降级，绝不连累调用方。
            warn("读取玩家名册快照异常，降级返空：${t.message}")
            emptyMap()
        }
    }
}
