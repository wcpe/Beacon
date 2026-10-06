package top.wcpe.beacon.agent.api;

import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.Map;

/**
 * 调度候选的只读快照视图（FR-148）。
 *
 * <p>字段与控制面候选契约对齐（{@code v2-metrics-health-scheduling} §5.1）：仅 schedulable / degraded 候选进入快照。
 * 本视图为本机缓存的一帧，非实时——最长滞后一个刷新周期。</p>
 *
 * <p><b>标签字段（FR-244）</b>：{@link #labels()} 是该候选节点<b>自己声明</b>的键值标签
 * （FR-243 的写入面，见 ADR-0086），<b>不是</b>控制面另登记的一套。调用方可在<b>选之前</b>
 * 用它自行收窄候选集合；若希望收窄发生在<b>决策内部</b>（"被排除的候选连被选中的机会都没有"），
 * 用 {@link BeaconScheduling#acquireCandidate(String, String, AdmissionScope)} 把同样的条件带进去。</p>
 */
public final class CandidateView {

    private final String serverId;
    private final String zone;
    private final int score;
    private final HealthLevel level;
    private final int onlineCount;
    private final int maxOnline;

    /** 节点自声明的键值标签；恒非 null，本帧没有可展示的标签时为空 map（不等于"节点没声明过"）。 */
    private final Map<String, String> labels;

    /**
     * 不带标签的候选视图（既有构造器，<b>行为逐位不变</b>）——标签记为空 map。
     *
     * <p>保留它是为了二进制兼容：既有调用方（含只读缓存的旧字段集）不必改动即可继续编译与运行。
     * 空 map 的含义见 {@link #labels()}：它是"本视图这一帧没有可展示的标签"，
     * <b>不是</b>对这个节点有没有声明过标签下结论。</p>
     */
    public CandidateView(String serverId, String zone, int score, HealthLevel level,
                         int onlineCount, int maxOnline) {
        this(serverId, zone, score, level, onlineCount, maxOnline, null);
    }

    /**
     * 带节点自声明标签的候选视图（FR-244 新增构造器）。
     *
     * @param labels 该节点自声明的键值标签；{@code null} 归为空 map，非 null 时做防御性拷贝并冻结。
     *               空 map 的含义见 {@link #labels()}
     */
    public CandidateView(String serverId, String zone, int score, HealthLevel level,
                         int onlineCount, int maxOnline, Map<String, String> labels) {
        this.serverId = serverId;
        this.zone = zone;
        this.score = score;
        this.level = level;
        this.onlineCount = onlineCount;
        this.maxOnline = maxOnline;
        // 防御性拷贝：调用方后续改动入参 map 不影响本不可变值对象。
        this.labels = labels == null || labels.isEmpty()
                ? Collections.<String, String>emptyMap()
                : Collections.unmodifiableMap(new LinkedHashMap<String, String>(labels));
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

    /**
     * 该候选节点<b>自己声明</b>的键值标签（FR-243 的写入面，本仓不解释任何 key 的语义）。
     *
     * <p>返回不可修改的 map：既不可增删改，也不随控制面后续数据变化。</p>
     *
     * <p><b>空 map 只表示"本视图这一帧没有可展示的标签"，不是"这个节点没有声明过标签"</b>：
     * 它既可能来自"节点确实没有声明"，也可能来自"本帧快照来自不支持该字段的旧控制面"
     * （甚至节点声明了空标签集）——本视图<b>看不到</b>这三者的差别。"这一帧到底带没带
     * {@code labels} 字段"由来源层判定（core 侧的 {@code CandidateEntry.labelsPresent}），
     * 不在这里猜。</p>
     *
     * <p>因此<b>不要把本方法的空 map 当成"没有声明"的判据</b>：需要"按作用域收窄"时，
     * 一律走 {@link BeaconScheduling#acquireCandidate(String, String, AdmissionScope)}
     * （或 {@link BeaconScheduling#candidatesInZone(String, AdmissionScope)}），
     * 由实现方按可用的判据决定是"收窄后确实没有候选"还是"这一帧判不了"，
     * 两者在返回值与异常上是分开的。</p>
     *
     * <p>规模上限对齐 FR-227：单个 key 长度 {@code ≤ 32}、单个 value 长度 {@code ≤ 128}、
     * 单节点标签数 {@code ≤ 20}。</p>
     */
    public Map<String, String> labels() {
        return labels;
    }
}
