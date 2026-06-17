package top.wcpe.beacon.agent.kit;

import top.wcpe.beacon.agent.api.AgentUnavailableException;
import top.wcpe.beacon.agent.api.BeaconAgent;
import top.wcpe.beacon.agent.api.BeaconAgentProvider;
import top.wcpe.beacon.agent.api.ConfigChangeListener;
import top.wcpe.beacon.agent.api.EffectiveConfig;
import top.wcpe.beacon.agent.api.ListenerHandle;

import java.util.HashSet;
import java.util.List;
import java.util.Set;

/**
 * 配置变更订阅句柄（{@link AutoCloseable}，由调用方保管并 {@link #close()}）。
 *
 * <p>封装下游订阅样板，解决两类竞态：</p>
 * <ul>
 *   <li><b>注册即重放</b>：底层 {@link EffectiveConfig#onChange} 只回调「之后」的变更，
 *       首次注册成功时本句柄会用「当前有效配置」立即重放一次，下游无需自行先读一遍再订阅。</li>
 *   <li><b>转可用补注册</b>：subscribe 时若 agent 尚未就绪，本句柄不会丢订阅，而是延迟到 agent
 *       由不可用转可用时（含 reload 后的新门面实例）补注册底层监听并重放当前值。</li>
 * </ul>
 *
 * <p>补注册由下游周期调用 {@link #pump()} 驱动——kit 不自起线程、不碰调度（零三方依赖约束）。
 * 下游在其既有 tick / 心跳里顺手调一次即可，成本极低、线程切换留在下游可控处。</p>
 *
 * <p>本句柄方法线程安全：内部对状态变更同步，便于「下游 tick 线程驱动 pump」与
 * 「agent 异步线程触发底层回调」并存。回调本身仅转发，重活仍需下游自行切线程。</p>
 */
public final class BeaconSubscription implements AutoCloseable {

    /** 下游传入的变更监听器（重放与透传都回调它）。 */
    private final ConfigChangeListener listener;

    /** 当前已注册的底层监听句柄；未注册时为空。 */
    private ListenerHandle handle;

    /** 已注册监听所对应的底层 EffectiveConfig 实例；用于识别 agent 换实例（reload）。 */
    private EffectiveConfig boundConfig;

    /** close 后置位，拒绝后续补注册与重放。 */
    private boolean closed;

    BeaconSubscription(ConfigChangeListener listener) {
        this.listener = listener;
        // 构造即尝试注册一次：若 agent 已就绪则立即注册底层监听并重放当前值；
        // 未就绪则什么都不做，等下游 pump 驱动补注册。
        pump();
    }

    /**
     * 推进订阅状态：若 agent 已就绪而本句柄尚未绑定到其当前门面，则注册底层监听并重放一次当前值。
     *
     * <p>由下游在既有 tick / 心跳里周期调用，用于补上「subscribe 时 agent 未就绪」「agent reload 换新门面」
     * 两类竞态。已绑定且门面未变时本方法为幂等空操作。close 后为空操作。</p>
     */
    public synchronized void pump() {
        if (closed) {
            return;
        }
        if (!BeaconAgentProvider.isAvailable()) {
            // agent 不在场：丢弃可能已失效的旧绑定（旧门面实例已注销，其监听无意义），
            // 等其再次就绪时重新补注册重放。
            forgetBinding();
            return;
        }

        BeaconAgent agent;
        EffectiveConfig config;
        try {
            agent = BeaconAgentProvider.get();
            config = agent.config();
        } catch (AgentUnavailableException ignored) {
            // isAvailable 与 get 之间发生注销的竞态：本轮放弃，下轮 pump 再试。
            forgetBinding();
            return;
        }

        if (handle != null && config == boundConfig) {
            // 已绑定到当前门面，无需重复注册。
            return;
        }
        // 门面换了实例（reload）或首次注册：先解绑旧的再绑新的。
        forgetBinding();
        handle = config.onChange(listener);
        boundConfig = config;
        replayCurrent(agent, config);
    }

    /** 注销底层监听并清空绑定，重复调用安全。 */
    @Override
    public synchronized void close() {
        closed = true;
        forgetBinding();
    }

    /** 解绑当前底层监听（若有）。 */
    private void forgetBinding() {
        if (handle != null) {
            handle.remove();
            handle = null;
        }
        boundConfig = null;
    }

    /** 用当前有效配置（全部 dataId + 整体 md5）向监听器重放一次，使订阅方拿到注册时的基线。 */
    private void replayCurrent(BeaconAgent agent, EffectiveConfig config) {
        List<String> dataIds = config.dataIds();
        // 整体 md5 尚未收敛时用空串占位，仍重放一次让下游知道「已订阅、当前为空」。
        String md5 = agent.effectiveMd5().orElse("");
        Set<String> changed = new HashSet<>(dataIds);
        listener.onConfigChanged(changed, md5);
    }
}
