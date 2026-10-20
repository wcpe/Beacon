package top.wcpe.beacon.agent.api;

import java.util.Set;

/**
 * 可上报重载结果的配置变更回调。
 *
 * <p>相对既有的 {@link ConfigChangeListener}，本接口让业务插件把「本次重载是否成功、为什么失败、
 * 实际加载了什么」返回给 agent，从而让交付控制面拿到真实生效结果，而不是只看到「通知已派发」。</p>
 *
 * <p><b>为什么要用子接口而不是改老接口</b>：老插件只实现 {@link ConfigChangeListener#onConfigChanged}，
 * 改签名会破坏其二进制兼容；本接口继承老接口并提供默认实现，故：</p>
 * <ul>
 *   <li>老插件：零改动，继续用 {@code onChange(ConfigChangeListener)} 注册。</li>
 *   <li>新插件：实现 {@link #onConfigChangedReporting} 即可；注册路径完全不变，
 *       agent 侧识别为上报式监听器后即取用返回结果。</li>
 * </ul>
 *
 * <p><b>纪律</b>：</p>
 * <ul>
 *   <li>回调在 agent 异步线程触发，重活请自行切到业务线程；但**不要阻塞超过
 *       配置重载超时**，否则本次重载按失败计。</li>
 *   <li>失败请**显式返回** {@code ConfigReloadResult.failed(...)} 并给可读原因；
 *       抛异常虽由 agent 兜住并计为失败，但会丢失原因信息。</li>
 *   <li>已成功应用新配置时，建议用 {@code withAppliedMd5/withAppliedVersion}
 *       回报实际加载内容，供控制面核对「回执成功但版本不符」。</li>
 * </ul>
 */
public interface ReportingConfigChangeListener extends ConfigChangeListener {

    /**
     * 配置已变更，请把新配置应用到运行态并返回结果。
     *
     * @param changedDataIds 本次变更的 dataId 集合
     * @param newMd5         变更后整体 md5（小写 hex）
     * @return 重载结果；返回 null 由 agent 按失败处置（缺结果不得被当作成功）
     */
    ConfigReloadResult onConfigChangedReporting(Set<String> changedDataIds, String newMd5);

    /**
     * 默认实现委托给上报方法并丢弃结果，保证老式「只发通知」调用语义仍可工作。
     */
    @Override
    default void onConfigChanged(Set<String> changedDataIds, String newMd5) {
        onConfigChangedReporting(changedDataIds, newMd5);
    }
}
