package top.wcpe.beacon.agent.api;

/**
 * 调度候选的只读快照视图（FR-148）。
 *
 * <p>字段与控制面候选契约对齐（{@code v2-metrics-health-scheduling} §5.1）：仅 schedulable / degraded 候选进入快照。
 * 本视图为本机缓存的一帧，非实时——最长滞后一个刷新周期。</p>
 */
public final class CandidateView {

    private final String serverId;
    private final String zone;
    private final int score;
    private final HealthLevel level;
    private final int onlineCount;
    private final int maxOnline;

    public CandidateView(String serverId, String zone, int score, HealthLevel level,
                         int onlineCount, int maxOnline) {
        this.serverId = serverId;
        this.zone = zone;
        this.score = score;
        this.level = level;
        this.onlineCount = onlineCount;
        this.maxOnline = maxOnline;
    }

    /** 候选子服 serverId。 */
    public String serverId() {
        return serverId;
    }

    /** 候选所在小区名。 */
    public String zone() {
        return zone;
    }

    /** 健康分（0-100）。 */
    public int score() {
        return score;
    }

    /** 健康等级。 */
    public HealthLevel level() {
        return level;
    }

    /** 在线人数（仅展示，不参与调用方决策）。 */
    public int onlineCount() {
        return onlineCount;
    }

    /** 容量上限。 */
    public int maxOnline() {
        return maxOnline;
    }
}
