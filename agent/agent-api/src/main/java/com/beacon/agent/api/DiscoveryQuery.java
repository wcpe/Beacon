package com.beacon.agent.api;

import java.util.Optional;

/**
 * 服务发现查询条件（不可变，经 {@link Builder} 构造）。
 *
 * <p>各字段为空表示该维度不过滤。无 canary。</p>
 */
public final class DiscoveryQuery {

    private final String namespace;
    private final String group;
    private final String zone;
    private final String role;

    private DiscoveryQuery(Builder builder) {
        this.namespace = builder.namespace;
        this.group = builder.group;
        this.zone = builder.zone;
        this.role = builder.role;
    }

    public Optional<String> namespace() {
        return Optional.ofNullable(namespace);
    }

    public Optional<String> group() {
        return Optional.ofNullable(group);
    }

    public Optional<String> zone() {
        return Optional.ofNullable(zone);
    }

    public Optional<String> role() {
        return Optional.ofNullable(role);
    }

    public static Builder builder() {
        return new Builder();
    }

    /** {@link DiscoveryQuery} 构造器。未设置的字段表示该维度不过滤。 */
    public static final class Builder {

        private String namespace;
        private String group;
        private String zone;
        private String role;

        private Builder() {
        }

        public Builder namespace(String namespace) {
            this.namespace = namespace;
            return this;
        }

        public Builder group(String group) {
            this.group = group;
            return this;
        }

        public Builder zone(String zone) {
            this.zone = zone;
            return this;
        }

        public Builder role(String role) {
            this.role = role;
            return this;
        }

        public DiscoveryQuery build() {
            return new DiscoveryQuery(this);
        }
    }
}
