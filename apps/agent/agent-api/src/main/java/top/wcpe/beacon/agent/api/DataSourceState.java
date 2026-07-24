package top.wcpe.beacon.agent.api;

/**
 * 本机候选缓存当前的数据来源状态与快照年龄（FR-148）。
 *
 * <p>业务插件据此判断数据新鲜度：{@link #fresh()} 为 false 表示快照已超龄（默认 &gt; 10 分钟），
 * 但仍可用（fail-static 优先可用性）。</p>
 */
public final class DataSourceState {

    private final DataSource source;
    private final boolean fresh;
    private final long snapshotAgeMs;

    public DataSourceState(DataSource source, boolean fresh, long snapshotAgeMs) {
        this.source = source;
        this.fresh = fresh;
        this.snapshotAgeMs = snapshotAgeMs;
    }

    /** 当前数据来源。 */
    public DataSource source() {
        return source;
    }

    /** 快照年龄是否在阈值内（默认 ≤ 10 分钟为 true；超龄仍可用但标 false）。 */
    public boolean fresh() {
        return fresh;
    }

    /** 最近一次候选快照落点距今毫秒；从无快照时为 {@link Long#MAX_VALUE}。 */
    public long snapshotAgeMs() {
        return snapshotAgeMs;
    }
}
