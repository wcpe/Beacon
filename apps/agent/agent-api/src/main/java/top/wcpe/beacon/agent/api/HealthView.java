package top.wcpe.beacon.agent.api;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;

/**
 * 某台服务器的健康视图快照（FR-147 / FR-148）。
 *
 * <p>由 {@link BeaconScheduling#healthOf(String)} 与 {@link BeaconScheduling#selfHealth()} 返回，
 * 为本机缓存快照，非实时。{@code reasons} 为不可调度原因码列表（可空为空表）。</p>
 */
public final class HealthView {

    private final String serverId;
    private final int score;
    private final HealthLevel level;
    private final boolean schedulable;
    private final List<String> reasons;
    private final long sampledAtMs;

    public HealthView(String serverId, int score, HealthLevel level, boolean schedulable,
                      List<String> reasons, long sampledAtMs) {
        this.serverId = serverId;
        this.score = score;
        this.level = level;
        this.schedulable = schedulable;
        // 防御性拷贝为不可变列表：对外只读、避免外部改动本机缓存（null 归一为空表）。
        this.reasons = reasons == null
                ? Collections.<String>emptyList()
                : Collections.unmodifiableList(new ArrayList<String>(reasons));
        this.sampledAtMs = sampledAtMs;
    }

    /** 服务器 serverId。 */
    public String serverId() {
        return serverId;
    }

    /** 健康分（0-100）。 */
    public int score() {
        return score;
    }

    /** 健康等级。 */
    public HealthLevel level() {
        return level;
    }

    /** 是否可调度。 */
    public boolean schedulable() {
        return schedulable;
    }

    /** 不可调度原因码（只读，可空为空表）。 */
    public List<String> reasons() {
        return reasons;
    }

    /** 该视图对应的采样时刻（毫秒）。 */
    public long sampledAtMs() {
        return sampledAtMs;
    }
}
