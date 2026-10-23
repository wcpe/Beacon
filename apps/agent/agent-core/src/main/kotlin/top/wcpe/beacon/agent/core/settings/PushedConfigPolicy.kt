package top.wcpe.beacon.agent.core.settings

/**
 * 下发配置的**键级防护**：哪些键允许由控制面下发、哪些永远不许。
 *
 * ## 为什么必须有这一层
 *
 * 配置下发通道把「控制面」提升成了 agent 运行参数的权威来源。这在便利性上是收益，
 * 但在信任模型上引入了一个新的攻击面：**控制面（或被攻破的控制面）能改什么**。
 *
 * 有两个键一旦可下发，控制面就能反过来劫持 agent 本身：
 *
 * - `beacon.endpoints`：改掉它 = 把 agent 指向另一个控制面，之后所有命令、所有下发的
 *   配置、所有交付动作都听那个控制面的。这是**目标劫持**。
 * - `beacon.bootstrap-token`：改掉它 = 用别的身份接入，或让 agent 再也接不回来。
 *   这是**身份劫持**。
 *
 * 因此这两个键的权威只能来自**本地**（config.yml 或 join 命令写入的凭据文件），
 * 控制面无权修改。这与 ADR-0011「白名单放本地作反向制衡」是同一个思路：
 * 有些东西必须留在被管理端，否则「管理面被攻破」会直接等价于「被管理端被接管」。
 *
 * ## 断连保护
 *
 * 下发缓存（`pushed-config.json`）只在**成功拉取**时更新，且不含上述敏感键。
 * 断连时 agent 继续用缓存工作，但**永远不会**因缓存而改变自己的接入目标——
 * 因为目标只从本地读。
 */
object PushedConfigPolicy {
    /**
     * 禁止由控制面下发的键前缀与键名。
     *
     * 用「键」而非「正则」列出，是为了让审核者能一眼看完整个禁区。
     */
    private val forbiddenKeys: Set<String> =
        setOf(
            // 接入目标：改了就等于把 agent 交给另一个控制面。
            "beacon.endpoints",
            // 接入凭据：改了就等于换身份或让 agent 失联。
            "beacon.bootstrap-token",
            // 本地身份标识：它是「本机是谁」的唯一事实，只能由本地生成/迁移。
            "identity.server-id",
            "identity.identity-id",
        )

    /**
     * 禁止下发的键**前缀**：整组语义都属本地权威。
     *
     * `identity.` 整段禁掉的理由：身份字段由控制面在注册/审批流程里权威回填，
     * 而不是走「配置下发」这条旁路——两条通道改同一个字段必然打架。
     */
    private val forbiddenPrefixes: List<String> = listOf("identity.")

    /**
     * 该键是否允许下发。
     *
     * 未知键默认**允许**（新的可调参数不需要改这里就能下发），
     * 只有明确列入禁区/前缀的才拒绝——这样新增配置项不会被这条规则默默挡住。
     */
    fun isPushable(key: String): Boolean {
        val normalized = key.trim()
        // 单点返回：空键、命中禁区键、命中禁区前缀，三者都判不可下发。
        return normalized.isNotEmpty() &&
            normalized !in forbiddenKeys &&
            forbiddenPrefixes.none { normalized.startsWith(it) }
    }

    /**
     * 过滤掉不可下发的键，返回 `(允许的, 被拒的)`。
     *
     * 调用方（拉取下发配置时）用它把危险键挡在落盘之前，并对被拒的键告警——
     * **静默丢弃**是不可接受的：运维在 Web 上改了却"没生效"，且没有任何提示，
     * 会让人怀疑整条下发通道坏了。
     */
    fun partition(values: Map<String, String>): Pair<Map<String, String>, List<String>> {
        val allowed = LinkedHashMap<String, String>(values.size)
        val rejected = mutableListOf<String>()
        for ((k, v) in values) {
            if (isPushable(k)) {
                allowed[k] = v
            } else {
                rejected += k
            }
        }
        return allowed to rejected
    }

    /** 禁区键清单（供文档与诊断展示；不暴露内部可变结构）。 */
    fun forbidden(): Set<String> = forbiddenKeys.toSet()
}
