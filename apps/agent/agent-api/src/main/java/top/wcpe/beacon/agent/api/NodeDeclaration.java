package top.wcpe.beacon.agent.api;

import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.Optional;

/**
 * 节点自声明（FR-243，见 ADR-0086）：业务插件为本节点声明「容量」与「自定义键值标签」。
 *
 * <p><b>部分刷新</b>：两类字段各自可选——<b>缺省即不刷新该字段</b>（不是刷成空）。其中
 * {@link #capacity()} 用 {@link Optional} 与「容量 0」区分；{@link #labels()} 用
 * {@link #hasLabels()} 与「提交空标签集（= 清空全部标签）」区分。</p>
 *
 * <p><b>不解释语义</b>：Beacon 只承载「节点声明了哪些 key=value」，不定义任何 key 的业务含义，
 * 由接入方在自己的命名空间下自行解释（ADR-0086 §5）。</p>
 *
 * <p><b>有界</b>：标签沿用 FR-227 既有约束（key ≤ 32 / value ≤ 128 / 单节点 ≤ 20 个标签），
 * 超界由控制面按「声明被拒」处置（门面取值 {@code REJECTED}）。</p>
 *
 * <p>值对象不可变：标签在构造时防御性拷贝并包装为不可修改视图，构造后外部改动不影响本对象。</p>
 */
public final class NodeDeclaration {

    /** 本次要刷新的容量；null = 本次不刷新容量（与容量 0 不同）。 */
    private final Integer capacity;

    /** 本次要刷新的标签整体集合；null = 本次不刷新标签（与「空集合 = 清空」不同）。 */
    private final Map<String, String> labels;

    private NodeDeclaration(Integer capacity, Map<String, String> labels) {
        this.capacity = capacity;
        this.labels = labels;
    }

    /**
     * 只刷新容量（如 {@code NodeDeclaration.ofCapacity(100)}）。
     *
     * @param capacity 新容量；{@code 0} 是合法值，语义为「容量为 0」，与「不刷新」不同
     */
    public static NodeDeclaration ofCapacity(int capacity) {
        return new NodeDeclaration(capacity, null);
    }

    /**
     * 只刷新标签（提供即<b>整体替换</b>，不合并）。
     *
     * @param labels 新的完整标签集；空 {@code Map} 表示清空该节点全部标签
     */
    public static NodeDeclaration ofLabels(Map<String, String> labels) {
        return new NodeDeclaration(null, copyOf(labels));
    }

    /**
     * 同时刷新容量与标签。
     *
     * @param capacity 新容量（{@code 0} 合法）
     * @param labels   新的完整标签集（空 {@code Map} 表示清空全部标签）
     */
    public static NodeDeclaration of(int capacity, Map<String, String> labels) {
        return new NodeDeclaration(capacity, copyOf(labels));
    }

    /**
     * 本次声明的容量。
     *
     * @return 空 = 本次<b>不刷新</b>容量（区别于 {@code Optional.of(0)}）
     */
    public Optional<Integer> capacity() {
        return Optional.ofNullable(capacity);
    }

    /**
     * 本次声明是否携带标签。
     *
     * @return true = 标签随本次声明下发（整体替换，空 {@link #labels()} 即清空）；false = 不刷新标签
     */
    public boolean hasLabels() {
        return labels != null;
    }

    /**
     * 本次声明的标签整体集合。
     *
     * <p>{@link #hasLabels()} 为 false 时返回空 {@code Map}（此时其含义是「不刷新」而非「清空」，
     * 判定一律以 {@link #hasLabels()} 为准）。返回值为不可修改视图，绝不外抛可变引用。</p>
     */
    public Map<String, String> labels() {
        return labels == null ? Collections.<String, String>emptyMap() : labels;
    }

    /** 防御性拷贝为不可修改视图（null 原样透传，保留「未提供」语义）。 */
    private static Map<String, String> copyOf(Map<String, String> labels) {
        if (labels == null) {
            return null;
        }
        return Collections.unmodifiableMap(new LinkedHashMap<String, String>(labels));
    }
}
