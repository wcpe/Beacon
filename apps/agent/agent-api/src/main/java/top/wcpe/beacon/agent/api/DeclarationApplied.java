package top.wcpe.beacon.agent.api;

import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.Optional;

/**
 * 一次声明<b>生效后</b>的取值（控制面 200 响应回带，自证刷新结果；FR-243）。
 *
 * <p>与 {@link NodeDeclaration} 的区别是语义而非形状：本类是「刷新后的当前事实」（容量缺失表示
 * 控制面未回带该字段，而非「本次不刷新」），{@link NodeDeclaration} 是「本次要刷新哪些字段」。</p>
 *
 * <p>值对象不可变：标签在构造时防御性拷贝并包装为不可修改视图。</p>
 */
public final class DeclarationApplied {

    /** 生效容量；null = 控制面未回带该字段。 */
    private final Integer capacity;

    /** 生效标签整体集合（非 null，可能为空 = 该节点当前无标签）。 */
    private final Map<String, String> labels;

    /**
     * @param capacity 生效容量（可空，控制面未回带时为 null）
     * @param labels   生效标签整体集合（可空视为空集合）
     */
    public DeclarationApplied(Integer capacity, Map<String, String> labels) {
        this.capacity = capacity;
        Map<String, String> source = labels == null ? Collections.<String, String>emptyMap() : labels;
        this.labels = Collections.unmodifiableMap(new LinkedHashMap<String, String>(source));
    }

    /** 生效容量；控制面未回带该字段时为空。 */
    public Optional<Integer> capacity() {
        return Optional.ofNullable(capacity);
    }

    /** 生效标签整体集合（不可修改视图；空 Map = 该节点当前无标签）。 */
    public Map<String, String> labels() {
        return labels;
    }
}
