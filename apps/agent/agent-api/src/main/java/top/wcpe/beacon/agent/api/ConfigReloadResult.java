package top.wcpe.beacon.agent.api;

import java.util.Optional;
import java.util.OptionalInt;

/**
 * 一次配置重载的结果，由业务插件在 {@link ReportingConfigChangeListener} 中返回。
 *
 * <p>语义约定（见 docs/specs/delivery-reload-ack.md §3.1）：</p>
 * <ul>
 *   <li><b>成功</b>：{@link #ok()}，插件已把新配置真正应用到运行态。</li>
 *   <li><b>失败</b>：{@link #failed(String)}，必须给出可读原因；控制面按「一票否决」处置，
 *       该目标整体判失败并熔断停批。</li>
 *   <li><b>自报事实</b>：{@link #withAppliedMd5} / {@link #withAppliedVersion} /
 *       {@link #withAppliedCount} 用于回报「实际加载了什么」，供控制面核对
 *       「回执成功但版本不符」（不得把加载了旧内容的成功回执当作真成功）。</li>
 * </ul>
 *
 * <p><b>兼容性</b>：本类是纯 Java 8 不可变值对象，零第三方依赖。抛出异常等同
 * {@code failed}（由 agent 侧兜住），但推荐显式返回 {@code failed} 以保留可读原因。</p>
 */
public final class ConfigReloadResult {

    /** 空串代表「无原因 / 无详情」，避免调用方判空。 */
    private static final String EMPTY = "";

    private final boolean ok;
    private final String reason;
    private final String detailJson;
    private final String appliedMd5;
    private final String appliedVersion;
    private final Integer appliedCount;

    private ConfigReloadResult(
            boolean ok,
            String reason,
            String detailJson,
            String appliedMd5,
            String appliedVersion,
            Integer appliedCount) {
        this.ok = ok;
        this.reason = reason == null ? EMPTY : reason;
        this.detailJson = detailJson == null ? EMPTY : detailJson;
        this.appliedMd5 = appliedMd5;
        this.appliedVersion = appliedVersion;
        this.appliedCount = appliedCount;
    }

    /** 重载成功。 */
    public static ConfigReloadResult ok() {
        return new ConfigReloadResult(true, EMPTY, EMPTY, null, null, null);
    }

    /**
     * 重载成功，并附带结构化详情。
     *
     * @param detailJson 插件自定义的 JSON 文本，控制面原样透传不解析；可为 null
     */
    public static ConfigReloadResult ok(String detailJson) {
        return new ConfigReloadResult(true, EMPTY, detailJson, null, null, null);
    }

    /**
     * 重载失败。
     *
     * @param reason 可读原因（会直接进交付回执与告警文案，请勿放敏感信息）
     */
    public static ConfigReloadResult failed(String reason) {
        return new ConfigReloadResult(false, reason, EMPTY, null, null, null);
    }

    /**
     * 重载失败，并附带结构化详情。
     *
     * @param reason     可读原因
     * @param detailJson 插件自定义的 JSON 文本，控制面原样透传不解析；可为 null
     */
    public static ConfigReloadResult failed(String reason, String detailJson) {
        return new ConfigReloadResult(false, reason, detailJson, null, null, null);
    }

    /** 是否成功。 */
    public boolean isOk() {
        return ok;
    }

    /** 可读原因；成功时为空串（永不为 null）。 */
    public String reason() {
        return reason;
    }

    /** 插件自定义结构化详情 JSON；无则空串（永不为 null）。 */
    public String detailJson() {
        return detailJson;
    }

    /** 实际加载内容的 md5；未自报时为空。 */
    public Optional<String> appliedMd5() {
        return Optional.ofNullable(appliedMd5);
    }

    /** 实际解析出的规则/配置版本标识；未自报时为空。 */
    public Optional<String> appliedVersion() {
        return Optional.ofNullable(appliedVersion);
    }

    /** 实际加载的条目/活动数量；未自报时为空。 */
    public OptionalInt appliedCount() {
        return appliedCount == null ? OptionalInt.empty() : OptionalInt.of(appliedCount.intValue());
    }

    /**
     * 回报「实际加载内容的 md5」，供控制面核对版本是否符合期望。
     *
     * @return 新的结果实例（本类不可变）
     */
    public ConfigReloadResult withAppliedMd5(String md5) {
        return new ConfigReloadResult(ok, reason, detailJson, md5, appliedVersion, appliedCount);
    }

    /**
     * 回报「实际解析出的版本标识」，供控制面核对版本是否符合期望。
     *
     * @return 新的结果实例（本类不可变）
     */
    public ConfigReloadResult withAppliedVersion(String version) {
        return new ConfigReloadResult(ok, reason, detailJson, appliedMd5, version, appliedCount);
    }

    /**
     * 回报「实际加载的条目/活动数量」。
     *
     * @return 新的结果实例（本类不可变）
     */
    public ConfigReloadResult withAppliedCount(int count) {
        return new ConfigReloadResult(ok, reason, detailJson, appliedMd5, appliedVersion, Integer.valueOf(count));
    }

    @Override
    public String toString() {
        return "ConfigReloadResult{ok=" + ok
                + ", reason='" + reason + '\''
                + ", appliedMd5=" + appliedMd5
                + ", appliedVersion=" + appliedVersion
                + ", appliedCount=" + appliedCount
                + '}';
    }
}
