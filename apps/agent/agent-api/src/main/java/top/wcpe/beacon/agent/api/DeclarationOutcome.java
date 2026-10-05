package top.wcpe.beacon.agent.api;

import java.util.Optional;

/**
 * 声明结果（FR-243，见 ADR-0086 决策 3）：<b>取值层面区分三类结论</b>。
 *
 * <p>「通道不可用」（可重试）与「声明被拒」（稳定事实，改正后重报）的处置完全不同，
 * 因此<b>不得用同一个返回值承载两类失败</b>——{@link #applied()} 只在
 * {@link Status#APPLIED} 时有值，{@link #rejectReason()} 只在 {@link Status#REJECTED} 时有值，
 * 两者的取值集合没有交集。</p>
 *
 * <p>控制面状态码 → 门面取值的映射（真源见规格 §3.4）：</p>
 * <ul>
 *   <li>200 → {@link Status#APPLIED}（回带生效值）</li>
 *   <li>400 → {@link Status#REJECTED}（带拒绝原因：格式非法 / 超界 / 键不合法等）</li>
 *   <li>401 / 404 → {@link Status#UNAVAILABLE}（token 缺错 / 实例不在册，退避后重报）</li>
 *   <li>连接失败或尚未就绪 → {@link Status#UNAVAILABLE}</li>
 * </ul>
 */
public final class DeclarationOutcome {

    /** 声明结论三档。 */
    public enum Status {
        /** 已生效：控制面已刷新并回带生效值（{@link DeclarationOutcome#applied()} 必有值）。 */
        APPLIED,

        /** 声明被拒：请求体非法 / 超界等稳定事实，改正后重报（{@link DeclarationOutcome#rejectReason()} 必有值）。 */
        REJECTED,

        /** 通道不可用：尚未就绪 / 鉴权不过 / 实例不在册 / 连接失败，可退避后重报（无附加取值）。 */
        UNAVAILABLE,
    }

    private final Status status;
    private final DeclarationApplied applied;
    private final String rejectReason;

    /**
     * @param status       结论
     * @param applied      APPLIED 时的生效值（其它结论必须为 null）
     * @param rejectReason REJECTED 时的拒绝原因（其它结论必须为 null）
     */
    private DeclarationOutcome(Status status, DeclarationApplied applied, String rejectReason) {
        this.status = status;
        this.applied = applied;
        this.rejectReason = rejectReason;
    }

    /** 声明已生效（回带控制面生效值）。 */
    public static DeclarationOutcome applied(DeclarationApplied applied) {
        return new DeclarationOutcome(Status.APPLIED, applied, null);
    }

    /** 声明被拒（带原因，改正后可重报）。 */
    public static DeclarationOutcome rejected(String reason) {
        return new DeclarationOutcome(Status.REJECTED, null, reason);
    }

    /** 通道不可用（可退避后重报）。 */
    public static DeclarationOutcome unavailable() {
        return new DeclarationOutcome(Status.UNAVAILABLE, null, null);
    }

    /** 结论。 */
    public Status status() {
        return status;
    }

    /** 生效后的取值；仅 {@link Status#APPLIED} 时非空。 */
    public Optional<DeclarationApplied> applied() {
        return Optional.ofNullable(applied);
    }

    /**
     * 拒绝原因；仅 {@link Status#REJECTED} 时非空。
     *
     * <p>文案已脱敏（不携带任何凭据），供业务侧日志与运维定位「为什么没成功」。</p>
     */
    public Optional<String> rejectReason() {
        return Optional.ofNullable(rejectReason);
    }
}
