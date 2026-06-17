package top.wcpe.beacon.agent.kit;

import top.wcpe.beacon.agent.api.AgentIdentity;
import top.wcpe.beacon.agent.api.AgentUnavailableException;
import top.wcpe.beacon.agent.api.BeaconAgent;
import top.wcpe.beacon.agent.api.BeaconAgentProvider;
import top.wcpe.beacon.agent.api.ConfigChangeListener;
import top.wcpe.beacon.agent.api.DiscoveryQuery;
import top.wcpe.beacon.agent.api.ServiceInstance;

import java.util.Collections;
import java.util.List;
import java.util.Optional;

/**
 * Beacon 下游接入便捷门面。
 *
 * <p>把业务插件接入 Beacon 的样板收口成几个「正确」的便捷方法，杜绝常见踩坑——尤其是
 * <b>回退判据只看 {@link BeaconAgentProvider#isAvailable()}、绝不看 {@link BeaconAgent#connected()}</b>。
 * 控制面短暂不可用时 agent 仍用本地快照 fail-static、配置仍可读，若误用 {@code connected()} 当回退判据
 * 会把这种「在场但暂未连上」误判为「不可用」而回退到本地文件，造成 split-brain。</p>
 *
 * <p>本类<b>不是有状态静态单例</b>：可自由 {@code new}，内部不缓存 agent 引用（每次调用现取门面，
 * 避免持有已注销的旧实例）。订阅返回 {@link BeaconSubscription} 句柄，由调用方保管并 {@code close()}。</p>
 *
 * <p>边界（守 ADR-0005，实现不进 kit）：</p>
 * <ul>
 *   <li>kit 不碰线程调度（不引 okhttp/kotlinx/TabooLib）。发现查询走同步 HTTP，<b>请在异步线程调用</b>；
 *       变更回调在 agent 异步线程触发，重活下游自行切线程。</li>
 *   <li><b>本地文件回退不在 kit</b>：agent 不在场时读配置便捷方法返回空，要不要回退本地文件、怎么读，
 *       由下游自行决定（kit 只用 {@link #isBeaconPresent()} 告知是否在场）。</li>
 *   <li>身份/zone/ORM 仍归 CoreLib；{@link #identity()} 仅薄转发，不重复实现。</li>
 * </ul>
 */
public final class BeaconAccess {

    public BeaconAccess() {
        // 无状态门面，无需初始化。
    }

    /**
     * Beacon agent 是否在场（下游回退判据）。
     *
     * <p><b>只看 {@link BeaconAgentProvider#isAvailable()}</b>。为 {@code false} 时下游可回退到本地文件；
     * 为 {@code true} 时即便控制面暂未连上（fail-static）也应继续走 Beacon，不要回退（防 split-brain）。</p>
     */
    public boolean isBeaconPresent() {
        return BeaconAgentProvider.isAvailable();
    }

    /** 当前 agent 身份（薄转发）；agent 不在场时为空。 */
    public Optional<AgentIdentity> identity() {
        BeaconAgent agent = peek();
        return agent == null ? Optional.empty() : Optional.of(agent.identity());
    }

    /** 取某 dataId 的合并后原始文本；agent 不在场或无此项时为空。 */
    public Optional<String> rawConfig(String dataId) {
        BeaconAgent agent = peek();
        return agent == null ? Optional.empty() : agent.config().raw(dataId);
    }

    /** 取某 dataId 的格式（yaml / properties / json）；agent 不在场或无此项时为空。 */
    public Optional<String> configFormat(String dataId) {
        BeaconAgent agent = peek();
        return agent == null ? Optional.empty() : agent.config().format(dataId);
    }

    /** 取某 dataId 的单项 md5；agent 不在场或无此项时为空。 */
    public Optional<String> configMd5(String dataId) {
        BeaconAgent agent = peek();
        return agent == null ? Optional.empty() : agent.config().md5(dataId);
    }

    /** 列出当前所有有效配置项的 dataId；agent 不在场时为空列表。 */
    public List<String> dataIds() {
        BeaconAgent agent = peek();
        return agent == null ? Collections.<String>emptyList() : agent.config().dataIds();
    }

    /** 当前有效配置整体 md5；agent 不在场或尚未收敛时为空。 */
    public Optional<String> effectiveMd5() {
        BeaconAgent agent = peek();
        return agent == null ? Optional.empty() : agent.effectiveMd5();
    }

    /**
     * 订阅有效配置变更。
     *
     * <p>注册即重放当前值；subscribe 时 agent 未就绪不会丢订阅，由下游周期调用
     * {@link BeaconSubscription#pump()} 在 agent 转可用后补注册并重放（见其文档）。</p>
     *
     * @return 订阅句柄，由调用方保管并 {@code close()}
     */
    public BeaconSubscription subscribeConfig(ConfigChangeListener listener) {
        if (listener == null) {
            throw new IllegalArgumentException("监听器不能为空");
        }
        return new BeaconSubscription(listener);
    }

    /**
     * 按条件过滤在线实例（同步 HTTP，<b>请在异步线程调用</b>）；agent 不在场时为空列表。
     */
    public List<ServiceInstance> query(DiscoveryQuery query) {
        BeaconAgent agent = peek();
        return agent == null ? Collections.<ServiceInstance>emptyList() : agent.discovery().query(query);
    }

    /** 列出某 zone 下在线实例（同步 HTTP，请在异步线程调用）；agent 不在场时为空列表。 */
    public List<ServiceInstance> instancesInZone(String group, String zone) {
        BeaconAgent agent = peek();
        return agent == null
                ? Collections.<ServiceInstance>emptyList()
                : agent.discovery().instancesInZone(group, zone);
    }

    /** 列出某大区(group)下全部在线实例（同步 HTTP，请在异步线程调用）；agent 不在场时为空列表。 */
    public List<ServiceInstance> instancesInGroup(String group) {
        BeaconAgent agent = peek();
        return agent == null
                ? Collections.<ServiceInstance>emptyList()
                : agent.discovery().instancesInGroup(group);
    }

    /**
     * 现取门面：不在场返回 {@code null}（便捷方法据此降级为空，不向下游抛 {@link AgentUnavailableException}）。
     *
     * <p>用 try/catch 吞 {@link AgentUnavailableException} 应对 isAvailable 与 get 之间的注销竞态——
     * 这是「正常降级」而非吞业务异常。</p>
     */
    private BeaconAgent peek() {
        if (!BeaconAgentProvider.isAvailable()) {
            return null;
        }
        try {
            return BeaconAgentProvider.get();
        } catch (AgentUnavailableException ignored) {
            return null;
        }
    }
}
