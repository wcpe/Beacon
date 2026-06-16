package com.beacon.agent.api;

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

    public ServiceInstance(String serverId, String role, String group, String zone, String address,
                           String version, String status, int playerCount, int capacity, int weight) {
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
}
