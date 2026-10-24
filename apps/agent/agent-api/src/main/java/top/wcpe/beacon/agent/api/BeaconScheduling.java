package top.wcpe.beacon.agent.api;

import java.util.List;
import java.util.concurrent.CompletableFuture;

/**
 * 本机调度 / 健康门面（FR-148）：业务插件取调度候选与健康事实的唯一入口。
 *
 * <p>业务插件<b>禁止直连 Beacon HTTP</b>（直连不作为契约、随时可变）；HTTP / JSON 实现只存在于 agent 适配器
 * （[ADR-0005] 延续），本接口不暴露任何传输细节，对业务插件<b>只读</b>——无改配置 / 改 zone 旁路。</p>
 *
 * <p><b>降级语义（fail-static）</b>：控制面不可用时——{@link #acquireCandidate} /
 * {@link #acquireCandidateInLobby} / {@link #acquireCandidateInSharedPool} 走本地快照决策照常返回；
 * {@link #candidatesInZone} / {@link #candidatesInLobby} / {@link #candidatesInSharedPool} /
 * {@link #healthOf} 继续供给最后快照
 * （含 agent 重启后落盘恢复）；一切方法
 * <b>不抛因控制面不可达导致的异常、不阻塞玩家进服链路</b>；控制面恢复后自动切回在线决策并补报降级期决策。</p>
 *
 * <p><b>三种调度作用域</b>：按小区（{@code acquireCandidate} / {@code candidatesInZone}，zone 名定位）、
 * 按<b>入口大厅池</b>（{@code acquireCandidateInLobby} / {@code candidatesInLobby}，FR-281）
 * 与按<b>共享游戏池</b>（{@code acquireCandidateInSharedPool} / {@code candidatesInSharedPool}，FR-281）——
 * 后两者面向"多个大区共享的一批公共服"，成员不属于任何小区，故取候选与读候选都不接受 zone。
 * 两池的区别在<b>用途</b>：入口大厅池是 BC <b>首次落脚</b>的地方，共享游戏池是<b>跨区共享的游戏服池</b>
 * （选中即开游戏房）；两者成员集合<b>互斥</b>（控制面归属四选一保证），故互不兜底。</p>
 */
public interface BeaconScheduling {

    /**
     * 在指定小区内取一台可调度候选。
     *
     * <p>异步返回（内部走独立线程，绝不阻塞调用线程与 MC 主线程）；控制面不可用时自动降级为本地快照决策，
     * future 仍正常完成（fail-static，不因控制面不可达异常完成）。等价于 {@code acquireCandidate(zone, null)}。</p>
     *
     * @param zone 目标小区名（namespace 内唯一）
     * @return 决策结果 future；{@link ScheduleResult#chosen()} 为 null 表示本次未取到候选
     */
    CompletableFuture<ScheduleResult> acquireCandidate(String zone);

    /**
     * 在指定小区内取一台可调度候选，并携带业务用途说明。
     *
     * @param zone    目标小区名
     * @param purpose 业务用途说明（可空，如 {@code lobby-transfer}），随决策记录入库供排查
     * @return 决策结果 future
     */
    CompletableFuture<ScheduleResult> acquireCandidate(String zone, String purpose);

    /**
     * 在指定小区内取一台可调度候选，并带上<b>准入作用域</b>——决策在<b>生成候选的那一刻</b>就按它收窄
     * （FR-244）。
     *
     * <p><b>为什么要有它</b></p>
     *
     * <p>不带作用域时，"这台候选到底接不接我这个区 / 具不具备这个资格"只有调用方自己知道，
     * 于是只能走「先选中、再由调用方校验并拒掉」：被排除的候选仍然<b>占过一次被选中的机会</b>，
     * 结果是本次调用白跑一圈直接失败。带上作用域后，不满足条件的节点<b>根本不进入候选集合</b>。</p>
     *
     * <p><b>语义</b></p>
     *
     * <p>作用域是<b>一组节点自声明键值标签的 AND 要求</b>（见 {@link AdmissionScope}）：
     * 候选节点的自声明必须<b>全部</b>满足它才进入决策；空作用域（{@link AdmissionScope#empty()}）
     * 与不带作用域的那条重载<b>逐位等价</b>。本仓<b>不解释</b>任何 key 的业务含义（FR-243 同口径）。</p>
     *
     * <p><b>作用域把候选全滤掉时</b>：{@link ScheduleResult#state()} 为
     * {@link ScheduleState#NO_CANDIDATE}（稳定事实，重试无用），
     * {@link ScheduleResult#failReason()} 为 {@code no_candidate_in_scope}——
     * 它与"这个小区本来就没有候选"（{@code no_candidate}）是两条不同的诊断，
     * 但结论类别相同（都不可重试）。</p>
     *
     * <p><b>向后兼容</b></p>
     *
     * <p>本方法是 {@code default}：既有实现在<b>不重写它</b>的前提下仍能编译、仍能作为
     * {@link BeaconAgent#scheduling()} 的返回值使用。</p>
     *
     * <p>缺省实现按作用域分两路，方向由"会不会放宽准入"决定：</p>
     *
     * <ul>
     *   <li><b>空作用域</b>（{@code null} 或 {@link AdmissionScope#isEmpty()}）：
     *       直接委派给 {@link #acquireCandidate(String, String)}——空作用域<b>不排除任何候选</b>，
     *       委派与实现方自己按空作用域决策<b>逐位等价</b>，不会放宽任何准入。
     *       这条正是老实现方调 {@code acquireCandidate(zone, purpose, AdmissionScope.empty())}
     *       时应当得到的等价结果，而不是一个异常。</li>
     *   <li><b>非空作用域</b>：抛 {@link UnsupportedOperationException}——因为"忽略作用域、照旧全量决策"
     *       是<b>静默放宽准入</b>，比响亮地不支持更危险。调用方据此把这种情况归入"本实现不提供该能力"。</li>
     * </ul>
     *
     * <p>既有三个方法（{@code acquireCandidate(String)} / {@code acquireCandidate(String, String)} /
     * {@code candidatesInZone(String)}）的<b>签名一字未改</b>。可观测取值有一处按 FR-244 口径更正：
     * 实现方在"看不到"（门面未就绪 / 判据不可用）时原先回 {@code failReason = "no_candidate"}
     * （"确实没有候选"，稳定事实），现更正为 {@code "unavailable"} + {@link ScheduleState#UNAVAILABLE}
     * （当前状态、可重试）——把可重试处境印成稳定结论会让调用方永久放弃一次本来只是环境未就绪的派房。</p>
     *
     * @param zone    目标小区名（namespace 内唯一）
     * @param purpose 业务用途说明（可空）
     * @param scope   准入作用域；{@code null} 视为 {@link AdmissionScope#empty()}
     * @return 决策结果 future（fail-static 契约不变：不因控制面不可达而异常完成）
     * @throws UnsupportedOperationException 本实现不支持按作用域收窄，且本次作用域<b>非空</b>
     *                                       （结论不可用，调用方自行降级）
     */
    default CompletableFuture<ScheduleResult> acquireCandidate(String zone, String purpose, AdmissionScope scope) {
        if (scope == null || scope.isEmpty()) {
            // 空作用域无约束力：等价于不带作用域的那条重载，委派不会放宽准入。
            return acquireCandidate(zone, purpose);
        }
        throw new UnsupportedOperationException(
                "本 BeaconScheduling 实现不支持准入作用域（acquireCandidate/3）："
                        + "忽略作用域并照旧全量决策会让调用方的准入过滤静默失效，故按不支持处理。"
                        + "调用方应改用不带作用域的那条重载，并把准入校验放回自己那一侧（失败关闭）");
    }

    /**
     * 列出指定小区当前候选快照，并只保留<b>满足准入作用域</b>的那些（FR-244）。
     *
     * <p>判定用的标签是候选节点<b>自己声明</b>的那一份（{@link CandidateView#labels()} 同一真源），
     * 因此"这次读到的候选集合"与"决策会考虑的候选集合"是同一套条件，
     * 不会出现"候选列表里有、决策却选不中"这种两处口径分叉。</p>
     *
     * <p><b>向后兼容</b>：同为 {@code default}，缺省实现与
     * {@link #acquireCandidate(String, String, AdmissionScope)} 同一条方向原则——
     * <b>空作用域</b>（{@code null} 或 {@link AdmissionScope#isEmpty()}）直接委派给
     * {@link #candidatesInZone(String)}（空作用域不排除任何候选，等价且不放宽）；
     * <b>非空作用域</b>抛 {@link UnsupportedOperationException}（忽略作用域返回全量候选是静默放宽准入）。
     * {@code candidatesInZone(String)} 的签名<b>一字未改</b>
     * （取值口径的更正见 {@link #acquireCandidate(String, String, AdmissionScope)}）。</p>
     *
     * <p><b>抛出契约（两类异常必须分别处置，不得合并）</b></p>
     *
     * <ul>
     *   <li>{@link UnsupportedOperationException}：<b>能力缺失</b>——本实现不支持按作用域收窄
     *       （例如旧实现、只看缓存字段集不全的实现）。这是"这条路本实现走不通"，
     *       调用方的正确动作是改用不带作用域的重载，并把准入校验放回自己那一侧
     *       （或按自己的能力探测结果换实现），<b>不是</b>"没有候选"。</li>
     *   <li>{@link IllegalStateException}：<b>本帧判据不可用</b>——本次收窄判不了，
     *       典型情形是候选快照未携带节点自声明标签字段（对端尚未支持该字段），
     *       即"判定依据这一帧看不到"。它是<b>当前状态、可重试</b>，
     *       调用方必须按"不可用"处置（与 {@link ScheduleState#UNAVAILABLE} 同口径：
     *       等下一帧刷新后重试或改走不带作用域的路径），<b>不得</b>把它读成"没有候选"
     *       而永久放弃一次派房。实现方<b>不得</b>用返回空列表来代替本异常——
     *       那是拿"看不到"冒充"没有"。</li>
     * </ul>
     *
     * <p>两者都不会因为"控制面不可达"而发生：控制面不可用时读本地快照照常返回
     * （fail-static），只有<b>判据本身缺失</b>才抛 {@code IllegalStateException}。</p>
     *
     * @param zone  目标小区名
     * @param scope 准入作用域；{@code null} 视为 {@link AdmissionScope#empty()}（等价于不带作用域的重载）
     * @return 满足作用域的候选（顺序与原快照一致）；缓存未覆盖该小区时为空列表（非 null）。
     *         作用域非空时"空列表"是<b>稳定事实</b>（收窄后的确没有符合条件的候选），
     *         与上面的 {@code IllegalStateException}（判不了）严格区分
     * @throws UnsupportedOperationException 本实现不支持按作用域收窄，且本次作用域<b>非空</b>（能力缺失）
     * @throws IllegalStateException 本次收窄的判据不可用（如候选快照未携带自声明标签字段，属可重试的当前状态）
     */
    default List<CandidateView> candidatesInZone(String zone, AdmissionScope scope) {
        if (scope == null || scope.isEmpty()) {
            // 空作用域无约束力：等价于不带作用域的那条重载，直接委派。
            return candidatesInZone(zone);
        }
        throw new UnsupportedOperationException(
                "本 BeaconScheduling 实现不支持按准入作用域收窄候选（candidatesInZone/2）："
                        + "忽略作用域返回全量候选会让调用方的准入过滤静默失效，故按不支持处理");
    }

    /**
     * 列出指定小区当前候选快照（本地缓存，O(1) 读，可在主线程调用）。
     *
     * <p>非实时，最长滞后一个刷新周期；缓存未覆盖该小区时返回空列表（非 null）。</p>
     */
    List<CandidateView> candidatesInZone(String zone);

    /**
     * 在 namespace 全局大厅池里取一台可调度候选（FR-281）。等价于
     * {@code acquireCandidateInLobby(purpose, null)}。
     *
     * <p><b>为什么要有它</b>：大厅成员（LobbyCluster）<b>不属于任何小区</b>——共享池正是靠这一点成立的。
     * 只带 {@code zone} 的那三条重载表达不了它，调用方只能自己拼一个假 zone 名（控制面会按小区查，
     * 结果是 zone_not_found），或者放弃走 Beacon 决策。本方法把"在池里挑一台"交回控制面决策：
     * 候选在<b>生成的那一刻</b>就被圈定，与小区档共用同一套健康 / 容量 / 排空判定。</p>
     *
     * <p><b>语义</b>：走控制面已有的 {@code scope=lobby} 分支（不新造协议）——只取<b>请求方 namespace
     * 唯一大厅集群</b>的成员；大厅为空是允许的配置中间态（返回"确实没有候选"这一稳定结论）。</p>
     *
     * <p><b>降级语义（fail-static）</b>：控制面不可用时用本地候选快照里的大厅段决策，future 仍正常完成；
     * 排序契约与 BC 首次落脚、控制面在线决策<b>同一个纯函数</b>（不另造一套大厅排序）。</p>
     *
     * @param purpose 业务用途说明（可空），随决策记录入库供排查
     * @return 决策结果 future（fail-static 契约与 zone 档一致：不因控制面不可达而异常完成）
     * @throws UnsupportedOperationException 本实现不提供大厅调度能力（见
     *                                       {@link #acquireCandidateInLobby(String, AdmissionScope)}）
     */
    default CompletableFuture<ScheduleResult> acquireCandidateInLobby(String purpose) {
        return acquireCandidateInLobby(purpose, null);
    }

    /**
     * 在 namespace 全局大厅池里取一台可调度候选，并带上<b>准入作用域</b>（FR-281）。
     *
     * <p>作用域语义与 {@link #acquireCandidate(String, String, AdmissionScope)} <b>逐字相同</b>
     * （空作用域逐位等价于不带作用域；不满足作用域的节点根本不进入候选集合）。同一条约束在大厅池上
     * 尤其实际：池是多个大区共享的，"这台节点此刻接不接我这一单"只有调用方自己知道。</p>
     *
     * <p><b>本方法是大厅能力的唯一实现点</b>（与 zone 档相反，这里带作用域的那条才是根）：
     * 大厅能力与作用域收窄是<b>同时引入</b>的，不存在"支持大厅但不支持按作用域收窄"的历史形态，
     * 故不需要为老实现留"先支持大厅、作用域以后再说"的中间态；{@link #acquireCandidateInLobby(String)}
     * 只是"空作用域"的糖，直接委派到本条。</p>
     *
     * <p><b>向后兼容</b>：本方法是 {@code default}，缺省实现<b>抛
     * {@link UnsupportedOperationException}</b>——"本实现不提供该能力"。这是三档结论里最响的一档：
     * 调用方据此知道要改用 zone 档或自行处置，<b>不会</b>被一个"忽略作用域 / 忽略大厅语义"的静默结果骗过。</p>
     *
     * <p><b>三态结论</b>（{@link ScheduleResult#state()}，与 zone 档同一套词表）：</p>
     * <ul>
     *   <li>{@link ScheduleState#CHOSEN}：选中了；{@link CandidateView#zone()} 为<b>空串</b>（大厅成员不属于任何小区）。</li>
     *   <li>{@link ScheduleState#NO_CANDIDATE}：<b>稳定事实</b>——问到了，池里（或收窄后）确实没有可调度成员。</li>
     *   <li>{@link ScheduleState#UNAVAILABLE}：<b>当前状态、可重试</b>——没问到。含"从没有过快照"、
     *       "本帧来源没有大厅模型"（旧控制面）、"控制面拒绝了本次请求"。</li>
     * </ul>
     *
     * <p><b>两者不得互相冒充</b>：大厅档的"问到了"只有一条路（200；池空也是 200 + 无候选），
     * 因此控制面回 4xx（例如对端不认识大厅作用域或参数被拒）一律归 {@link ScheduleState#UNAVAILABLE}，
     * {@link ScheduleResult#failReason()} 原样带出对端码——把"没问成"印成"确实没有候选"会让调用方
     * <b>永久放弃</b>一次本来只是环境未就绪的派房。</p>
     *
     * @param purpose 业务用途说明（可空）
     * @param scope   准入作用域；{@code null} 视为 {@link AdmissionScope#empty()}
     * @return 决策结果 future（fail-static 契约与 zone 档一致：不因控制面不可达而异常完成）
     * @throws UnsupportedOperationException 本实现不提供大厅调度能力
     */
    default CompletableFuture<ScheduleResult> acquireCandidateInLobby(String purpose, AdmissionScope scope) {
        throw new UnsupportedOperationException(
                "本 BeaconScheduling 实现不支持大厅作用域调度（acquireCandidateInLobby）："
                        + "大厅成员不属于任何小区，用一条构造出来的 zone 名替代只会得到"
                        + "「小区不存在」这种与控制面口径不符的假结论，故按不支持处理。"
                        + "调用方应改用带 zone 的重载，或按自己的能力探测结果换实现");
    }

    /**
     * 列出 namespace 全局大厅池当前候选快照，并只保留<b>满足准入作用域</b>的那些（FR-281）。
     *
     * <p>判定用的标签是候选节点<b>自己声明</b>的那一份（{@link CandidateView#labels()} 同一真源），
     * 与 {@link #candidatesInZone(String, AdmissionScope)} 同一口径：读到的候选集合与决策会考虑的
     * 候选集合是同一套条件。</p>
     *
     * <p><b>抛出契约（两类异常必须分别处置，不得合并）</b></p>
     * <ul>
     *   <li>{@link UnsupportedOperationException}：<b>能力缺失</b>——本实现不提供大厅候选读取；
     *       空作用域也不改变这一点（与 zone 档不同：zone 档的空作用域不碰判据，故照旧返回全量）。</li>
     *   <li>{@link IllegalStateException}：<b>本帧判据不可用</b>——<b>快照里没有大厅模型</b>
     *       （从未刷新成功，或本帧来源是还没有大厅模型的旧控制面），或作用域非空而候选缺
     *       自声明标签字段。它是<b>当前状态、可重试</b>，实现方<b>不得</b>用返回空表代替本异常——
     *       那是拿"看不到"冒充"池里没有机器"。注意与"大厅模型在、但池里确实没有可调度成员"
     *       严格区分：后者是<b>稳定事实</b>，照常返回空表。</li>
     * </ul>
     *
     * @param scope 准入作用域；{@code null} 视为 {@link AdmissionScope#empty()}
     * @return 满足作用域的大厅候选（顺序与原快照一致）；每个视图的 {@link CandidateView#zone()} 为<b>空串</b>
     * @throws UnsupportedOperationException 本实现不提供大厅候选读取
     * @throws IllegalStateException 本帧判据不可用（没有大厅模型，或候选缺自声明标签字段）
     */
    default List<CandidateView> candidatesInLobby(AdmissionScope scope) {
        throw new UnsupportedOperationException(
                "本 BeaconScheduling 实现不支持读取大厅候选（candidatesInLobby）："
                        + "返回空表会把「看不到大厅池」冒充成「池里没有机器」，故按不支持处理");
    }

    /**
     * 列出 namespace 全局大厅池当前候选快照（本地缓存，O(1) 读，可在主线程调用）。
     *
     * <p>等价于 {@code candidatesInLobby(null)}。大厅模型存在但池里没有可调度成员时返回空列表
     * （非 null）——那是<b>稳定事实</b>；本帧来源压根没有大厅模型时<b>抛
     * {@link IllegalStateException}</b>，绝不返回空表（见
     * {@link #candidatesInLobby(AdmissionScope)} 的抛出契约）。</p>
     *
     * @throws UnsupportedOperationException 本实现不提供大厅候选读取
     * @throws IllegalStateException 本帧没有大厅模型（判据看不到，可重试）
     */
    default List<CandidateView> candidatesInLobby() {
        return candidatesInLobby(null);
    }

    /**
     * 在 namespace 共享游戏池里取一台可调度候选（FR-281）。等价于
     * {@code acquireCandidateInSharedPool(purpose, null)}。
     *
     * <p><b>为什么要有它</b>：共享游戏池成员（SharedPool）<b>不属于任何小区</b>——跨区共享正是靠这一点成立的。
     * 只带 {@code zone} 的那三条重载表达不了它，调用方只能自己拼一个假 zone 名（控制面会按小区查，
     * 结果是 zone_not_found），或者放弃走 Beacon 决策。本方法把"在池里挑一台"交回控制面决策：
     * 候选在<b>生成的那一刻</b>就被圈定，与小区档共用同一套健康 / 容量 / 排空判定。</p>
     *
     * <p><b>与入口大厅池的区别</b>：{@link #acquireCandidateInLobby(String)} 面向的是 BC <b>首次落脚</b>
     * 的入口大厅（玩家先落到那里，再转进游戏）；本方法面向的是<b>跨区共享的游戏服池</b>，
     * 选中即开游戏房。两者是<b>互斥的两批成员</b>（控制面归属四选一保证：小区 / 大厅 / 共享池 / 独立），
     * 故两档的候选集合不会重叠，也<b>不该互相兜底</b>——拿大厅的机器去开游戏房，
     * 与拿共享池的机器去接 BC 首次落脚，都是把玩家送错地方。</p>
     *
     * <p><b>语义</b>：走控制面已有的 {@code scope=shared_pool} 分支（不新造协议）——只取<b>请求方 namespace
     * 唯一共享游戏池</b>的成员；池为空是允许的配置中间态（返回"确实没有候选"这一稳定结论）。</p>
     *
     * <p><b>降级语义（fail-static）</b>：控制面不可用时用本地候选快照里的共享池段决策，future 仍正常完成；
     * 排序契约与小区档、控制面在线决策<b>同一个纯函数</b>（不另造一套池内排序）。</p>
     *
     * @param purpose 业务用途说明（可空），随决策记录入库供排查
     * @return 决策结果 future（fail-static 契约与 zone 档一致：不因控制面不可达而异常完成）
     * @throws UnsupportedOperationException 本实现不提供共享游戏池调度能力（见
     *                                       {@link #acquireCandidateInSharedPool(String, AdmissionScope)}）
     */
    default CompletableFuture<ScheduleResult> acquireCandidateInSharedPool(String purpose) {
        return acquireCandidateInSharedPool(purpose, null);
    }

    /**
     * 在 namespace 共享游戏池里取一台可调度候选，并带上<b>准入作用域</b>（FR-281）。
     *
     * <p>作用域语义与 {@link #acquireCandidate(String, String, AdmissionScope)} <b>逐字相同</b>
     * （空作用域逐位等价于不带作用域；不满足作用域的节点根本不进入候选集合）。同一条约束在共享游戏池上
     * 尤其实际：池是多个大区共享的，"这台节点此刻接不接我这一单、开不开得了我要的那种游戏房"
     * 只有调用方自己知道。</p>
     *
     * <p><b>本方法是共享游戏池能力的唯一实现点</b>（与 zone 档相反，这里带作用域的那条才是根）：
     * 池能力与作用域收窄是<b>同时引入</b>的，不存在"支持共享池但不支持按作用域收窄"的历史形态，
     * 故不需要为老实现留"先支持共享池、作用域以后再说"的中间态；
     * {@link #acquireCandidateInSharedPool(String)} 只是"空作用域"的糖，直接委派到本条。</p>
     *
     * <p><b>向后兼容</b>：本方法是 {@code default}，缺省实现<b>抛
     * {@link UnsupportedOperationException}</b>——"本实现不提供该能力"。这是三档结论里最响的一档：
     * 调用方据此知道要改用 zone 档或自行处置，<b>不会</b>被一个"忽略作用域 / 忽略共享池语义"的静默结果骗过。</p>
     *
     * <p><b>三态结论</b>（{@link ScheduleResult#state()}，与 zone 档、入口大厅档同一套词表）：</p>
     * <ul>
     *   <li>{@link ScheduleState#CHOSEN}：选中了；{@link CandidateView#zone()} 为<b>空串</b>（池成员不属于任何小区）。</li>
     *   <li>{@link ScheduleState#NO_CANDIDATE}：<b>稳定事实</b>——问到了，池里（或收窄后）确实没有可调度成员。</li>
     *   <li>{@link ScheduleState#UNAVAILABLE}：<b>当前状态、可重试</b>——没问到。含"从没有过快照"、
     *       "本帧来源没有共享池模型"（旧控制面）、"控制面拒绝了本次请求"。</li>
     * </ul>
     *
     * <p><b>两者不得互相冒充</b>：共享池档的"问到了"只有一条路（200；池空也是 200 + 无候选），
     * 因此控制面回 4xx（例如对端不认识共享池作用域或参数被拒）一律归 {@link ScheduleState#UNAVAILABLE}，
     * {@link ScheduleResult#failReason()} 原样带出对端码——把"没问成"印成"确实没有候选"会让调用方
     * <b>永久放弃</b>一次本来只是环境未就绪的派房。</p>
     *
     * @param purpose 业务用途说明（可空）
     * @param scope   准入作用域；{@code null} 视为 {@link AdmissionScope#empty()}
     * @return 决策结果 future（fail-static 契约与 zone 档一致：不因控制面不可达而异常完成）
     * @throws UnsupportedOperationException 本实现不提供共享游戏池调度能力
     */
    default CompletableFuture<ScheduleResult> acquireCandidateInSharedPool(String purpose, AdmissionScope scope) {
        throw new UnsupportedOperationException(
                "本 BeaconScheduling 实现不支持共享游戏池作用域调度（acquireCandidateInSharedPool）："
                        + "共享池成员不属于任何小区，用一条构造出来的 zone 名替代只会得到"
                        + "「小区不存在」这种与控制面口径不符的假结论，故按不支持处理。"
                        + "调用方应改用带 zone 的重载，或按自己的能力探测结果换实现");
    }

    /**
     * 列出 namespace 共享游戏池当前候选快照，并只保留<b>满足准入作用域</b>的那些（FR-281）。
     *
     * <p>判定用的标签是候选节点<b>自己声明</b>的那一份（{@link CandidateView#labels()} 同一真源），
     * 与 {@link #candidatesInZone(String, AdmissionScope)} 同一口径：读到的候选集合与决策会考虑的
     * 候选集合是同一套条件。</p>
     *
     * <p><b>抛出契约（两类异常必须分别处置，不得合并）</b></p>
     * <ul>
     *   <li>{@link UnsupportedOperationException}：<b>能力缺失</b>——本实现不提供共享游戏池候选读取；
     *       空作用域也不改变这一点（与 zone 档不同：zone 档的空作用域不碰判据，故照旧返回全量）。</li>
     *   <li>{@link IllegalStateException}：<b>本帧判据不可用</b>——<b>快照里没有共享池模型</b>
     *       （从未刷新成功，或本帧来源是还没有共享池模型的旧控制面），或作用域非空而候选缺
     *       自声明标签字段。它是<b>当前状态、可重试</b>，实现方<b>不得</b>用返回空表代替本异常——
     *       那是拿"看不到"冒充"池里没有机器"。注意与"共享池模型在、但池里确实没有可调度成员"
     *       严格区分：后者是<b>稳定事实</b>，照常返回空表。</li>
     * </ul>
     *
     * @param scope 准入作用域；{@code null} 视为 {@link AdmissionScope#empty()}
     * @return 满足作用域的共享池候选（顺序与原快照一致）；每个视图的 {@link CandidateView#zone()} 为<b>空串</b>
     * @throws UnsupportedOperationException 本实现不提供共享游戏池候选读取
     * @throws IllegalStateException 本帧判据不可用（没有共享池模型，或候选缺自声明标签字段）
     */
    default List<CandidateView> candidatesInSharedPool(AdmissionScope scope) {
        throw new UnsupportedOperationException(
                "本 BeaconScheduling 实现不支持读取共享游戏池候选（candidatesInSharedPool）："
                        + "返回空表会把「看不到共享池」冒充成「池里没有机器」，故按不支持处理");
    }

    /**
     * 列出 namespace 共享游戏池当前候选快照（本地缓存，O(1) 读，可在主线程调用）。
     *
     * <p>等价于 {@code candidatesInSharedPool(null)}。共享池模型存在但池里没有可调度成员时返回空列表
     * （非 null）——那是<b>稳定事实</b>；本帧来源压根没有共享池模型时<b>抛
     * {@link IllegalStateException}</b>，绝不返回空表（见
     * {@link #candidatesInSharedPool(AdmissionScope)} 的抛出契约）。</p>
     *
     * @throws UnsupportedOperationException 本实现不提供共享游戏池候选读取
     * @throws IllegalStateException 本帧没有共享池模型（判据看不到，可重试）
     */
    default List<CandidateView> candidatesInSharedPool() {
        return candidatesInSharedPool(null);
    }

    /**
     * 查询某台服务器的健康视图（本地候选缓存快照）。
     *
     * <p>覆盖该服出现在候选快照里的<b>全部位置</b>：所在小区、全局大厅池与共享游戏池（FR-281）——
     * 两池成员都没有 zone，若只按小区找，池成员的 {@code healthOf} 会恒为 null。</p>
     *
     * @return 缓存未覆盖该服时返回 null
     */
    HealthView healthOf(String serverId);

    /**
     * 查询本服自身健康视图（随每次指标上报响应刷新，约 5s 新鲜度）。
     *
     * @return 从未上报成功时返回 null
     */
    HealthView selfHealth();

    /** 当前数据来源状态与快照年龄。 */
    DataSourceState dataSource();
}
