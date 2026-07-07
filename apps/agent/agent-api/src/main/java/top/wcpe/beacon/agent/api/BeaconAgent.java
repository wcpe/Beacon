package top.wcpe.beacon.agent.api;

import java.util.Optional;

/** Beacon agent 对业务插件暴露的总门面：读有效配置 + 查服务发现。MVP 只读不写。 */
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
     * <p>始终返回非 null；模块未开启或 Redis 未连上时其 {@link Messaging#isAvailable()} 为 false，
     * 业务插件据此优雅降级。</p>
     */
    Messaging messaging();

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
