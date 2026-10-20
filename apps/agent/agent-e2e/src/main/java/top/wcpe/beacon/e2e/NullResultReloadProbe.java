package top.wcpe.beacon.e2e;

import java.util.Set;
import top.wcpe.beacon.agent.api.ConfigReloadResult;
import top.wcpe.beacon.agent.api.ReportingConfigChangeListener;

/**
 * A5（缺结果）真机验收探针：实现 {@link ReportingConfigChangeListener} 但**返回 null**。
 *
 * <p>为什么必须用 Java 写：{@code onConfigChangedReporting} 的契约返回类型是非空的
 * {@code ConfigReloadResult}，Kotlin 侧有平台类型非空断言、无法合法返回 null。而 Java 实现可以，
 * 恰好复现「插件没给结果」这一真实失败模式。agent 侧必须按失败处置——
 * 缺结果不得被当作成功（护栏：不得假报成功）。</p>
 */
public final class NullResultReloadProbe implements ReportingConfigChangeListener {

    /** 受控路径：只对该 dataId 返回 null，避免影响其它用例。 */
    public static final String PATH = "plugins/BeaconE2E/delivery-reload-null.yml";

    @Override
    public ConfigReloadResult onConfigChangedReporting(Set<String> changedDataIds, String newMd5) {
        if (changedDataIds.contains(PATH)) {
            // 刻意不返回任何结果：agent 应按「缺结果」判失败。
            return null;
        }
        return ConfigReloadResult.ok();
    }
}
