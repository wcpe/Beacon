package com.beacon.agent.api;

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
     * agent 当前是否已连上控制面。
     *
     * <p>false 表示正在用本地快照 fail-static 运行——配置仍可读，但可能非最新。</p>
     */
    boolean connected();

    /** 当前有效配置整体 md5；尚无有效配置时为空。可用于业务侧判断是否已收敛。 */
    Optional<String> effectiveMd5();
}
