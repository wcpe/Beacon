package top.wcpe.beacon.agent.api;

import java.util.Optional;

/**
 * Beacon agent 对业务插件暴露的总门面：读有效配置 + 查服务发现 + 节点自声明。
 *
 * <p>边界（见 ADR-0086 与 architecture-invariants §1）：<b>只读 + 节点自声明的窄写入面</b>——
 * 只读部分不写任何控制面状态；唯一的写入面是业务插件刷新<b>自己节点</b>的容量与标签
 * （{@link #declaration()}），改配置 / 改 zone / 写他人一律不可达。</p>
 */
public interface BeaconAgent {

    /** 当前 agent 身份（namespace/serverId/role/group/zone）。 */
    AgentIdentity identity();

    /** 有效配置只读视图。 */
    EffectiveConfig config();

    /** 服务发现查询（同步 HTTP，请在异步线程调用）。 */
    Discovery discovery();

    /**
     * 跨服消息中间件门面（FR-26）：定向 / RPC / 主题 / 按玩家寻址的通用传输。
     *
     * <p>始终返回非 null；模块未开启或控制面不可达时其 {@link Messaging#isAvailable()} 为 false，
     * 业务插件据此优雅降级。</p>
     */
    Messaging messaging();

    /**
     * 本机调度 / 健康门面（FR-148）：取调度候选与健康事实的唯一入口。
     *
     * <p>始终返回非 null；控制面不可用时按本地快照 fail-static 降级，绝不阻塞玩家链路（见 {@link BeaconScheduling}）。</p>
     */
    BeaconScheduling scheduling();

    /**
     * 本节点自声明门面（FR-243，见 ADR-0086）：在运行期刷新自己的容量与自定义标签。
     *
     * <p>始终返回非 null；尚未注册成功 / 控制面不可达时返回降级实现，
     * 其 {@link SelfDeclaration#declare(NodeDeclaration)} 一律返回
     * {@link DeclarationOutcome.Status#UNAVAILABLE}（可重试），业务侧据此优雅降级。
     * 未注册期间发起的声明（最近一次）会在注册成功后由 agent 自动补报一次。</p>
     */
    SelfDeclaration declaration();

    /**
     * agent 当前是否已连上控制面。
     *
     * <p>false 表示正在用本地快照 fail-static 运行——配置仍可读，但可能非最新。</p>
     */
    boolean connected();

    /** 当前有效配置整体 md5；尚无有效配置时为空。可用于业务侧判断是否已收敛。 */
    Optional<String> effectiveMd5();

    /**
     * 有界等待 agent 首次注册成功（控制面权威应答返回、zone 已按响应回填）。
     *
     * <p>阻塞当前线程至多 {@code timeoutMillis} 毫秒；已就绪则立即返回。等待判据是「首次注册成功」
     * 而非「zone 非空」——未指派 zone 是合法终态。{@code timeoutMillis <= 0} 表示不阻塞、只查当前是否已就绪。</p>
     *
     * @return true=已就绪（注册成功过）；false=超时仍未就绪（如控制面不可用）
     */
    boolean awaitRegistered(long timeoutMillis);
}
