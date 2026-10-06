package top.wcpe.beacon.agent.api;

/**
 * 一次 {@link BeaconScheduling#acquireCandidate(String) acquireCandidate} 的决策结果（FR-148）。
 *
 * <p>无论控制面在线决策还是降级本地决策，future 均正常完成（fail-static，不因控制面不可达而异常完成）；
 * 调用方以 {@link #chosen()} 是否为 null 判断本次是否取到候选。</p>
 *
 * <p><b>三态结论（FR-244）</b>：{@code chosen() == null} 这一个事实承载了两种方向相反的处境——
 * 「本次作用域内确实没有候选」（稳定事实，重试无用）与「决策没做成、结论未知」（当前状态，可重试）。
 * 调用方一律读 {@link #state()} 区分，<b>不要</b>从 {@link #failReason()} 的字符串去猜。</p>
 */
public final class ScheduleResult {

    private final CandidateView chosen;
    private final String traceId;
    private final DecisionSource source;
    private final String failReason;

    /**
     * 结论类别；恒非 null，且与 {@link #chosen()} <b>始终一致</b>——{@code CHOSEN} 必有选中候选，
     * 另两态必无（自相矛盾的组合在构造时被拒，见带 state 的构造器）。
     */
    private final ScheduleState state;

    /** 本次**因准入作用域**被排除的候选台数（FR-244）；无作用域 / 未被收窄时为 0。 */
    private final int admissionExcludedCount;

    /**
     * 既有构造器（<b>行为逐位不变</b>）：结论类别按 {@code chosen} 推导——
     * 选中了即 {@link ScheduleState#CHOSEN}，未选中即 {@link ScheduleState#NO_CANDIDATE}。
     *
     * <p>保留它是为了二进制兼容既有调用方。它<b>表达不了</b> {@link ScheduleState#UNAVAILABLE}
     * （"决策没做成"）——那需要实现方显式用
     * {@link #ScheduleResult(CandidateView, String, DecisionSource, String, ScheduleState)} 说出来；
     * 用本构造器报"没做成"会把一次可重试的处境印成稳定结论。</p>
     */
    public ScheduleResult(CandidateView chosen, String traceId, DecisionSource source, String failReason) {
        this(chosen, traceId, source, failReason, chosen != null ? ScheduleState.CHOSEN : ScheduleState.NO_CANDIDATE);
    }

    /**
     * 带三态结论的构造器（FR-244 新增）。
     *
     * @param state 结论类别；实现方<b>必须</b>如实给出，不得把"决策没做成"报成
     *              {@link ScheduleState#NO_CANDIDATE}（那是另一类结论，恢复动作方向相反）；
     *              {@code null} 按 {@code chosen} 兜底推导（见
     *              {@link #ScheduleResult(CandidateView, String, DecisionSource, String, ScheduleState, int)}）
     * @throws IllegalArgumentException {@code state} 与 {@code chosen} 自相矛盾（一致性校验见 6 参构造器）
     */
    public ScheduleResult(CandidateView chosen, String traceId, DecisionSource source, String failReason,
                          ScheduleState state) {
        this(chosen, traceId, source, failReason, state, 0);
    }

    /**
     * 带三态结论与<b>作用域排除台数</b>的构造器（FR-244）。
     *
     * <p><b>三态一致性校验</b>：三态的价值就在于"取值即事实"，故本构造器拒绝自相矛盾的组合——
     * {@link ScheduleState#CHOSEN} 必须带选中候选，{@link ScheduleState#NO_CANDIDATE} /
     * {@link ScheduleState#UNAVAILABLE} 必须不带。否则调用方按 {@link #state()} 读到的结论
     * 与 {@link #chosen()} 表达的结论会打架，而两个读数都是一眼可信的，
     * 谁也不会想到要去交叉验证。{@code state} 为 {@code null} 时仍按 {@code chosen} 兜底推导
     * （选中了即 {@code CHOSEN}，未选中即 {@code NO_CANDIDATE}），故 {@code null} 不参与一致性校验。</p>
     *
     * @param admissionExcludedCount 本次因准入作用域被排除的候选台数；{@code < 0} 归 0。
     *                               它只统计**作用域那一类**排除（健康原因不算），故调用方可以据此
     *                               确定地说"这次收窄真的发生了"——那是"决策阶段收窄"在来源侧可读的痕迹
     * @throws IllegalArgumentException {@code state} 与 {@code chosen} 自相矛盾（见上的一致性校验）
     */
    public ScheduleResult(CandidateView chosen, String traceId, DecisionSource source, String failReason,
                          ScheduleState state, int admissionExcludedCount) {
        ScheduleState resolved =
                state == null ? (chosen != null ? ScheduleState.CHOSEN : ScheduleState.NO_CANDIDATE) : state;
        if (resolved == ScheduleState.CHOSEN && chosen == null) {
            throw new IllegalArgumentException(
                    "结论为 CHOSEN 时必须给出选中候选（chosen 不能为 null）：三态要求「取值即事实」");
        }
        if (resolved != ScheduleState.CHOSEN && chosen != null) {
            throw new IllegalArgumentException(
                    "结论为 " + resolved + "（" + (resolved == ScheduleState.NO_CANDIDATE
                            ? "稳定事实，重试无用" : "当前状态，可重试") + "）时不得携带选中候选（chosen 必须为 null）");
        }
        this.chosen = chosen;
        this.traceId = traceId;
        this.source = source;
        this.failReason = failReason;
        this.state = resolved;
        this.admissionExcludedCount = Math.max(admissionExcludedCount, 0);
    }

    /** 选中的候选；为 null 表示本次调度失败（见 {@link #failReason()} 与 {@link #state()}）。 */
    public CandidateView chosen() {
        return chosen;
    }

    /** 决策唯一标识：控制面决策为服务端 traceId，本地降级为本地 traceId。 */
    public String traceId() {
        return traceId;
    }

    /** 决策来源。 */
    public DecisionSource source() {
        return source;
    }

    /**
     * 失败原因码（如 {@code no_candidate} / {@code no_candidate_in_scope} / {@code zone_not_found}）；
     * 成功为 null。
     *
     * <p><b>它是给人读的诊断串，不是判据</b>：需要区分"空结果"与"没做成"一律读 {@link #state()}。</p>
     */
    public String failReason() {
        return failReason;
    }

    /**
     * 本次结论的类别（FR-244）：{@link ScheduleState#CHOSEN} / {@link ScheduleState#NO_CANDIDATE}
     * （稳定事实）/ {@link ScheduleState#UNAVAILABLE}（当前状态、可重试）。
     *
     * <p>它与 {@link #chosen()} 不会互相矛盾：{@code CHOSEN} 时 {@code chosen()} 恒非 null，
     * 另两态时 {@code chosen()} 恒为 null（构造时即校验）。</p>
     */
    public ScheduleState state() {
        return state;
    }

    /**
     * 本次<b>因准入作用域</b>被排除的候选台数（FR-244）；没带作用域、或带了但一台都没被排除时为 {@code 0}。
     *
     * <p>它是"<b>决策阶段确实收窄了</b>"在调用方这一侧可直接读到的痕迹：{@code > 0} 时调用方可以
     * 留一条读数行（本仓接入方就是这么用的），而不必去猜"对端到底有没有按作用域收窄"。
     * 健康原因造成的排除<b>不计入</b>本数。</p>
     */
    public int admissionExcludedCount() {
        return admissionExcludedCount;
    }
}
