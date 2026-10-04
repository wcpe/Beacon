package top.wcpe.beacon.agent.core.messaging

/**
 * 可选名册实现的可变持有者（FR-31），本身即一个降级版 [RosterDirectory]。
 *
 * [DiscoveryView][top.wcpe.beacon.agent.core.api.DiscoveryView] 在装配期即创建，而名册实现可能晚于
 * 装配就绪，故装配期把本持有者注入 DiscoveryView，壳层就绪后 [set] 实际实现、停止时 [reset] 复位。
 *
 * 两个平台壳层（Bukkit 子服 / Bungee 代理）都在**身份注册成功之后**注入控制面 HTTP 名册适配器
 * （名册端点按 v2 鉴权身份圈定 namespace，未注册时请求必被 401 拒绝），并在运行时停止 / 撤销时 [reset]。
 *
 * 优雅降级（与 [MessagingHolder] 同构）：未注入实现、或实现读取抛异常时，[snapshot] 返回空 Map、
 * 绝不外抛，业务插件据此走自身降级（守不变量 #5 fail-static）。
 *
 * @param warn WARN 日志回调（实现读取异常时记一行，默认无操作）
 */
class RosterDirectoryHolder(
    private val warn: (String) -> Unit = {},
) : RosterDirectory {
    /** 当前注入的名册实现；null 表示未注入（装配期/运行时未就绪时的降级态）。 */
    @Volatile
    private var current: RosterDirectory? = null

    /** 切换为活跃名册实现（壳层在名册就绪后注入）。 */
    fun set(directory: RosterDirectory) {
        current = directory
    }

    /** 复位为未注入（名册实现停止时调用）。 */
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
            // 名册读取异常：降级返空，业务插件据此降级，绝不连累调用方。
            warn("读取玩家名册快照异常，降级返空：${t.message}")
            emptyMap()
        }
    }
}
