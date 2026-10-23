package top.wcpe.beacon.agent.core.api

/**
 * 配置变更派发的聚合结果（见 `docs/specs/delivery-reload-ack.md` §3.2）。
 *
 * [EffectiveConfigView.fireChanged] 逐监听器隔离执行后返回本报告：任一监听器失败不再影响其余
 * 监听器收到通知（修复原裸循环实现中「先抛异常者让后续插件静默停留在旧配置」的缺陷）。
 *
 * 调用方据此决定降级行为：交付生效路径按「一票否决」回执 failed，周期同步路径只记日志不中断。
 */
data class ConfigReloadReport(
    /** 本次变更的 dataId 集合。 */
    val changedDataIds: Set<String>,
    /** 变更后整体 md5（小写 hex）。 */
    val newMd5: String,
    /** 逐个监听器的执行结果，顺序与注册顺序无关（ConcurrentHashMap 迭代序）。 */
    val outcomes: List<PluginReloadOutcome>,
) {
    /** 是否存在失败（含抛异常）。 */
    val hasFailures: Boolean get() = outcomes.any { !it.ok }

    /** 失败的监听器清单。 */
    val failures: List<PluginReloadOutcome> get() = outcomes.filter { !it.ok }

    /**
     * 失败摘要（用于回执 error 与日志），形如 `1/3 插件重载失败：A: 出错原因; B: ...`。
     * 无失败时为空串。
     */
    fun failureSummary(): String {
        if (!hasFailures) return ""
        return "${failures.size}/${outcomes.size} 插件重载失败：" +
            failures.joinToString("; ") { "${it.listener}: ${it.reason}" }
    }

    companion object {
        /**
         * 无监听器 / 不关心结果的实现可返回该值（无失败）。
         * 平台壳测试桩与「未装配派发能力」的实现用它可以保持签名而不制造伪造结果。
         */
        fun empty(
            changedDataIds: Set<String>,
            newMd5: String,
        ): ConfigReloadReport = ConfigReloadReport(changedDataIds, newMd5, emptyList())
    }
}

/**
 * 单个监听器的重载结果。
 *
 * @param listener     监听器实现类全名（失败定位必用，不含实例身份，脱敏安全）
 * @param ok           是否成功
 * @param reason       失败原因（可读、已脱敏）；成功为空串
 * @param timeout      是否因超过单监听器重载超时而失败（[reason] 同时带超时说明）
 * @param detailJson   插件自报的结构化详情 JSON；无则空串
 * @param appliedMd5   插件自报「实际加载内容的 md5」；未自报为空串
 * @param appliedVersion 插件自报「实际解析出的版本标识」；未自报为空串
 * @param appliedCount 插件自报「实际加载的条目/活动数量」；未知为 null（0 是合法值）
 */
data class PluginReloadOutcome(
    val listener: String,
    val ok: Boolean,
    val reason: String,
    val timeout: Boolean = false,
    val detailJson: String = "",
    val appliedMd5: String = "",
    val appliedVersion: String = "",
    val appliedCount: Int? = null,
) {
    companion object {
        fun ok(listener: String): PluginReloadOutcome = PluginReloadOutcome(listener, true, "")

        fun failed(
            listener: String,
            reason: String,
        ): PluginReloadOutcome = PluginReloadOutcome(listener, false, reason)
    }
}
