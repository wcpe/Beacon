package top.wcpe.beacon.agent.api;

import java.util.List;
import java.util.concurrent.CompletableFuture;

/**
 * 本机调度 / 健康门面（FR-148）：业务插件取调度候选与健康事实的唯一入口。
 *
 * <p>业务插件<b>禁止直连 Beacon HTTP</b>（直连不作为契约、随时可变）；HTTP / JSON 实现只存在于 agent 适配器
 * （[ADR-0005] 延续），本接口不暴露任何传输细节，对业务插件<b>只读</b>——无改配置 / 改 zone 旁路。</p>
 *
 * <p><b>降级语义（fail-static）</b>：控制面不可用时——{@link #acquireCandidate} 走本地快照决策照常返回；
 * {@link #candidatesInZone} / {@link #healthOf} 继续供给最后快照（含 agent 重启后落盘恢复）；一切方法
 * <b>不抛因控制面不可达导致的异常、不阻塞玩家进服链路</b>；控制面恢复后自动切回在线决策并补报降级期决策。</p>
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
     * 查询某台服务器的健康视图（本地候选缓存快照）。
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
