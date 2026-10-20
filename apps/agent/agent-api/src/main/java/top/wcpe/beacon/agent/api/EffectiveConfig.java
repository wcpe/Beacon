package top.wcpe.beacon.agent.api;

import java.util.List;
import java.util.Optional;

/**
 * 有效配置只读视图。
 *
 * <p>返回的是控制面按 scope 覆盖链「合并后」的最终文本，业务插件无需关心
 * global/group/zone/server 分层。MVP 仅暴露原始文本（{@link #raw(String)}），
 * 不做点分路径取值。</p>
 */
public interface EffectiveConfig {

    /** 列出当前所有有效配置项的 dataId。 */
    List<String> dataIds();

    /** 取某 dataId 的合并后原始文本；不存在返回空。 */
    Optional<String> raw(String dataId);

    /** 取某 dataId 的格式（yaml / properties / json）；不存在返回空。 */
    Optional<String> format(String dataId);

    /** 取某 dataId 的单项 md5；不存在返回空。 */
    Optional<String> md5(String dataId);

    /**
     * 注册有效配置变更监听（apply 后回调，携带变更的 dataId 集合）。
     *
     * <p>等价于 {@code onChange(null, listener)}：<b>不过滤</b>，任何 dataId 变更都会回调。
     * 想只被自己目录下的变更唤醒，用带前缀的 {@link #onChange(String, ConfigChangeListener)}。</p>
     *
     * @return 可注销句柄
     */
    ListenerHandle onChange(ConfigChangeListener listener);

    /**
     * 注册**按 dataId 路径前缀过滤**的有效配置变更监听。
     *
     * <p>语义：仅当本次变更的 dataId 集合中**存在以 {@code pathPrefix} 开头的项**时才回调该监听器；
     * 不相关就根本不调用（不是「调用了让你自己判断」——那正是本重载要消除的无谓唤醒）。
     * 回调收到的 {@code changedDataIds} 也**只含匹配的项**，插件无需再自行遍历筛选。</p>
     *
     * <p>{@code pathPrefix} 为 {@code null} 或空串时等于不过滤，行为与
     * {@link #onChange(ConfigChangeListener)} 完全一致。</p>
     *
     * <p><b>前缀请以 {@code /} 结尾</b>（如 {@code plugins/Foo/}）：这是字符串前缀匹配，
     * 写 {@code plugins/Foo} 会连 {@code plugins/Foo2/config.yml} 一起命中。
     * 匹配规则见 {@link DataIdPrefix}。</p>
     *
     * <p><b>为什么是新增重载而不是改老签名</b>：老签名被既有插件按 {@link ConfigChangeListener}
     * 单参调用，改签名会破坏其二进制兼容。故本方法用 {@code default} 提供降级实现——
     * 未实现过滤能力的旧 agent 会退化为「不过滤」（多几次无关唤醒，无正确性风险），
     * 新 agent 则真正跳过不相关派发。</p>
     *
     * @param pathPrefix 过滤前缀（如 {@code plugins/Foo/}）；{@code null} 或空串表示不过滤
     * @param listener   变更回调
     * @return 可注销句柄
     */
    default ListenerHandle onChange(String pathPrefix, ConfigChangeListener listener) {
        // 旧 agent 无过滤能力：降级为不过滤注册，保证新编译的插件仍能跑在旧 jar 上。
        return onChange(listener);
    }
}
