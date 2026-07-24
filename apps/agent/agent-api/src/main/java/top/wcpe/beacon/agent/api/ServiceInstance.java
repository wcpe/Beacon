package top.wcpe.beacon.agent.api;

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

    public ServiceInstance(String serverId, String role, String group, String zone, String address,
                           String version, String status, int playerCount, int capacity, int weight) {
        this(serverId, role, group, zone, address, version, status, playerCount, capacity, weight, false);
    }

    public ServiceInstance(String serverId, String role, String group, String zone, String address,
                           String version, String status, int playerCount, int capacity, int weight,
                           boolean zoneDefaultEntry) {
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
}
