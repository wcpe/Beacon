package top.wcpe.beacon.agent.api;

import java.util.Optional;

/**
 * 当前 agent 的身份。
 *
 * <p>{@code group}/{@code zone} 为控制面权威解析回填的值（agent 不声明 zone）；
 * 尚未指派 zone 时 {@link #zone()} 为空。</p>
 */
public final class AgentIdentity {

    private final String namespace;
    private final String serverId;
    private final String role;
    private final String group;
    private final String zone;

    public AgentIdentity(String namespace, String serverId, String role, String group, String zone) {
        this.namespace = namespace;
        this.serverId = serverId;
        this.role = role;
        this.group = group;
        this.zone = zone;
    }

    /** 环境（prod / test）。 */
    public String namespace() {
        return namespace;
    }

    /** 本机唯一身份，环境内唯一。 */
    public String serverId() {
        return serverId;
    }

    /** 角色（bukkit / bungee），由壳层固定。 */
    public String role() {
        return role;
    }

    /** 控制面解析的大区；未解析时为空。 */
    public Optional<String> group() {
        return Optional.ofNullable(group);
    }

    /** 控制面指派的小区；未指派时为空。 */
    public Optional<String> zone() {
        return Optional.ofNullable(zone);
    }
}
