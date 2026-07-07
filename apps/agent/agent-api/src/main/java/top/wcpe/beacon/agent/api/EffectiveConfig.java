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
     * @return 可注销句柄
     */
    ListenerHandle onChange(ConfigChangeListener listener);
}
