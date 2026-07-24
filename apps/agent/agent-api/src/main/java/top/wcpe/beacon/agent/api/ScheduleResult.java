package top.wcpe.beacon.agent.api;

/**
 * 一次 {@link BeaconScheduling#acquireCandidate(String) acquireCandidate} 的决策结果（FR-148）。
 *
 * <p>无论控制面在线决策还是降级本地决策，future 均正常完成（fail-static，不因控制面不可达而异常完成）；
 * 调用方以 {@link #chosen()} 是否为 null 判断本次是否取到候选。</p>
 */
public final class ScheduleResult {

    private final CandidateView chosen;
    private final String traceId;
    private final DecisionSource source;
    private final String failReason;

    public ScheduleResult(CandidateView chosen, String traceId, DecisionSource source, String failReason) {
        this.chosen = chosen;
        this.traceId = traceId;
        this.source = source;
        this.failReason = failReason;
    }

    /** 选中的候选；为 null 表示本次调度失败（见 {@link #failReason()}）。 */
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

    /** 失败原因码（如 {@code no_candidate} / {@code zone_not_found}）；成功为 null。 */
    public String failReason() {
        return failReason;
    }
}
