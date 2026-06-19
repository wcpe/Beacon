package top.wcpe.beacon.agent.api;

import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.Optional;

/**
 * 服务发现查询条件（不可变，经 {@link Builder} 构造）。
 *
 * <p>各字段为空表示该维度不过滤。tag 为自定义元数据键值过滤（多 tag 取交集，FR-29）。无 canary。</p>
 */
public final class DiscoveryQuery {

    private final String namespace;
    private final String group;
    private final String zone;
    private final String role;
    private final Map<String, String> tags;

    private DiscoveryQuery(Builder builder) {
        this.namespace = builder.namespace;
        this.group = builder.group;
        this.zone = builder.zone;
        this.role = builder.role;
        this.tags = Collections.unmodifiableMap(new LinkedHashMap<>(builder.tags));
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

    /** 自定义元数据过滤条件（不可变；空表示不按 tag 过滤）。多 tag 取交集。 */
    public Map<String, String> tags() {
        return tags;
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
        private final Map<String, String> tags = new LinkedHashMap<>();

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

        /** 追加一个自定义元数据过滤条件（key/value 任一为空则忽略）。 */
        public Builder tag(String key, String value) {
            if (key != null && !key.isEmpty() && value != null && !value.isEmpty()) {
                this.tags.put(key, value);
            }
            return this;
        }

        public DiscoveryQuery build() {
            return new DiscoveryQuery(this);
        }
    }
}
