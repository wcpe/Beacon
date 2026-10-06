package top.wcpe.beacon.agent.api;

/**
 * 一次选服决策的<b>结论类别</b>（FR-244）：把「有结果」「空结果」「没做成」三件事分成三个取值。
 *
 * <h2>为什么必须分开</h2>
 *
 * <p>调用方对三者的<b>恢复动作方向相反</b>：</p>
 *
 * <ul>
 *   <li>{@link #CHOSEN}：拿到了目标，照常往下走。</li>
 *   <li>{@link #NO_CANDIDATE}：<b>稳定事实</b>——本次作用域内确实没有满足条件的候选。
 *       重试不会改口，恢复动作是换区 / 调整服务范围 / 找人工，<b>不该重试</b>。</li>
 *   <li>{@link #UNAVAILABLE}：<b>当前状态</b>——门面或通道此刻不可用（超时 / 连接失败 / 读不出结论），
 *       结论<b>未知</b>。恢复动作是等通道恢复后重试，<b>不该</b>被读成「没有候选」而永久放弃一次派房。</li>
 * </ul>
 *
 * <p>拿同一个返回值（例如 {@code chosen() == null}）承载后两者，会让调用方把「看不到」印成「没有」，
 * 或反过来把「确实没有」当成「暂时不可用」去无限重试——两条路都静默。</p>
 */
public enum ScheduleState {

    /** 选出了候选（{@link ScheduleResult#chosen()} 非 null）。 */
    CHOSEN,

    /**
     * 决策成立、结论为「没有满足条件的候选」（{@link ScheduleResult#chosen()} 为 null）。
     *
     * <p>它与「作用域把候选全滤掉了」是同一类结论（都不可重试），具体是哪一种看
     * {@link ScheduleResult#failReason()}。</p>
     */
    NO_CANDIDATE,

    /** 决策没做成（结论未知，可重试）；{@link ScheduleResult#chosen()} 为 null。 */
    UNAVAILABLE
}
