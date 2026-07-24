package top.wcpe.beacon.agent.api;

import java.util.List;
import java.util.concurrent.CompletableFuture;

/**
 * 本机调度 / 健康门面（FR-148）：业务插件取调度候选与健康事实的唯一入口。
 *
 * <p>业务插件<b>禁止直连 Beacon HTTP</b>（直连不作为契约、随时可变）；HTTP / JSON 实现只存在于 agent 适配器
 * （[ADR-0005] 延续），本接口不暴露任何传输细节，对业务插件<b>只读</b>——无改配置 / 改 zone 旁路。</p>
 *
 * <p><b>降级语义（fail-static）</b>：控制面不可用时——{@link #acquireCandidate} 走本地快照决策照常返回；
 * {@link #candidatesInZone} / {@link #healthOf} 继续供给最后快照（含 agent 重启后落盘恢复）；一切方法
 * <b>不抛因控制面不可达导致的异常、不阻塞玩家进服链路</b>；控制面恢复后自动切回在线决策并补报降级期决策。</p>
 */
public interface BeaconScheduling {

    /**
     * 在指定小区内取一台可调度候选。
     *
     * <p>异步返回（内部走独立线程，绝不阻塞调用线程与 MC 主线程）；控制面不可用时自动降级为本地快照决策，
     * future 仍正常完成（fail-static，不因控制面不可达异常完成）。等价于 {@code acquireCandidate(zone, null)}。</p>
     *
     * @param zone 目标小区名（namespace 内唯一）
     * @return 决策结果 future；{@link ScheduleResult#chosen()} 为 null 表示本次未取到候选
     */
    CompletableFuture<ScheduleResult> acquireCandidate(String zone);

    /**
     * 在指定小区内取一台可调度候选，并携带业务用途说明。
     *
     * @param zone    目标小区名
     * @param purpose 业务用途说明（可空，如 {@code lobby-transfer}），随决策记录入库供排查
     * @return 决策结果 future
     */
    CompletableFuture<ScheduleResult> acquireCandidate(String zone, String purpose);

    /**
     * 列出指定小区当前候选快照（本地缓存，O(1) 读，可在主线程调用）。
     *
     * <p>非实时，最长滞后一个刷新周期；缓存未覆盖该小区时返回空列表（非 null）。</p>
     */
    List<CandidateView> candidatesInZone(String zone);

    /**
     * 查询某台服务器的健康视图（本地候选缓存快照）。
     *
     * @return 缓存未覆盖该服时返回 null
     */
    HealthView healthOf(String serverId);

    /**
     * 查询本服自身健康视图（随每次指标上报响应刷新，约 5s 新鲜度）。
     *
     * @return 从未上报成功时返回 null
     */
    HealthView selfHealth();

    /** 当前数据来源状态与快照年龄。 */
    DataSourceState dataSource();
}
