package top.wcpe.beacon.agent.api;

import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.Map;

/**
 * 服务发现返回的单个在线实例（只读值对象）。
 *
 * <p>字段与控制面 discovery 返回对齐：无 canary。</p>
 */
public final class ServiceInstance {

    private final String serverId;
    private final String role;
    private final String group;
    private final String zone;
    private final String address;
    private final String version;
    private final String status;
    private final int playerCount;
    private final int capacity;
    private final int weight;
    private final boolean zoneDefaultEntry;
    private final boolean lobbyClusterMember;
    private final Map<String, String> metadata;

    public ServiceInstance(String serverId, String role, String group, String zone, String address,
                           String version, String status, int playerCount, int capacity, int weight) {
        this(serverId, role, group, zone, address, version, status, playerCount, capacity, weight, false, false);
    }

    public ServiceInstance(String serverId, String role, String group, String zone, String address,
                           String version, String status, int playerCount, int capacity, int weight,
                           boolean zoneDefaultEntry) {
        this(serverId, role, group, zone, address, version, status, playerCount, capacity, weight,
                zoneDefaultEntry, false);
    }

    public ServiceInstance(String serverId, String role, String group, String zone, String address,
                           String version, String status, int playerCount, int capacity, int weight,
                           boolean zoneDefaultEntry, boolean lobbyClusterMember) {
        this(serverId, role, group, zone, address, version, status, playerCount, capacity, weight,
                zoneDefaultEntry, lobbyClusterMember, null);
    }

    /**
     * 带节点标签（metadata）的构造器。
     *
     * <p>旧构造器一律委托到这里并传 {@code null}，因此 {@code metadata} 恒非 null（缺省空 map），
     * 调用方无需做空判断。</p>
     *
     * @param metadata 节点声明的键值标签；{@code null} 归空 map，非 null 时做防御性拷贝并冻结为不可变
     */
    public ServiceInstance(String serverId, String role, String group, String zone, String address,
                           String version, String status, int playerCount, int capacity, int weight,
                           boolean zoneDefaultEntry, boolean lobbyClusterMember, Map<String, String> metadata) {
        this.serverId = serverId;
        this.role = role;
        this.group = group;
        this.zone = zone;
        this.address = address;
        this.version = version;
        this.status = status;
        this.playerCount = playerCount;
        this.capacity = capacity;
        this.weight = weight;
        this.zoneDefaultEntry = zoneDefaultEntry;
        this.lobbyClusterMember = lobbyClusterMember;
        // 防御性拷贝：调用方后续改动入参 map 不影响本不可变值对象。
        this.metadata = metadata == null || metadata.isEmpty()
                ? Collections.<String, String>emptyMap()
                : Collections.unmodifiableMap(new LinkedHashMap<String, String>(metadata));
    }

    public String serverId() {
        return serverId;
    }

    public String role() {
        return role;
    }

    public String group() {
        return group;
    }

    public String zone() {
        return zone;
    }

    public String address() {
        return address;
    }

    public String version() {
        return version;
    }

    /** 健康状态：online / lost / offline。 */
    public String status() {
        return status;
    }

    /** 在线人数（仅展示，不参与任何决策）。 */
    public int playerCount() {
        return playerCount;
    }

    public int capacity() {
        return capacity;
    }

    public int weight() {
        return weight;
    }

    /**
     * 该子服是否被指定为其小区（zone）的默认入口（FR-48）。
     *
     * <p>仅 {@code role=bukkit} 子服可能为 true；BC 代理 agent 据此把它设为 BungeeCord 默认/fallback 服。
     * 旧控制面不返回该字段时解析为 false（向后兼容）。</p>
     */
    public boolean zoneDefaultEntry() {
        return zoneDefaultEntry;
    }

    /**
     * 该子服是否属于 namespace 全局 LobbyCluster。
     *
     * <p>这是控制面发现快照的权威归属事实，与“当前可调度大厅候选”不同；后者受健康、容量和排水状态影响。
     * 旧控制面不返回该字段时解析为 false（向后兼容）。</p>
     */
    public boolean lobbyClusterMember() {
        return lobbyClusterMember;
    }

    /**
     * 节点声明的键值标签（只读事实）。
     *
     * <p>语义边界（务必按此理解，不要自行扩展）：这是<b>节点自己声明的只读事实</b>，
     * 控制面只存不判（不做任何语义校验与路由决策）；本对象<b>不解释任何 key 的语义</b>，
     * 例如不认 {@code region} / {@code tier} 之类的具体业务含义，只做键值透传。</p>
     *
     * <p>返回的 {@code Map} 不可变（构造时已防御性拷贝并冻结）：既不可增删改，
     * 也不会随控制面后续数据变化。旧控制面响应不带 {@code metadata} 字段时解析为空 map
     * （向后兼容，调用方无需判空）。</p>
     *
     * <p>规模上限对齐 FR-227：单个 key 长度 {@code ≤ 32}、单个 value 长度 {@code ≤ 128}、
     * 单节点标签数 {@code ≤ 20}。</p>
     *
     * @return 不可变的标签键值对；无标签时为空 map
     */
    public Map<String, String> metadata() {
        return metadata;
    }
}
