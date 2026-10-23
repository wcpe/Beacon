package top.wcpe.beacon.agent.kit;

import top.wcpe.beacon.agent.api.AgentUnavailableException;
import top.wcpe.beacon.agent.api.BeaconAgent;
import top.wcpe.beacon.agent.api.BeaconAgentProvider;
import top.wcpe.beacon.agent.api.ConfigChangeListener;
import top.wcpe.beacon.agent.api.DataIdPrefix;
import top.wcpe.beacon.agent.api.EffectiveConfig;
import top.wcpe.beacon.agent.api.ListenerHandle;

import java.util.LinkedHashSet;
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
 * <p><b>前缀过滤订阅</b>（{@code subscribeConfig(pathPrefix, listener)}）：前缀在句柄里**存住**，
 * 重放与补注册都按它裁剪——补注册不会把前缀弄丢（那会让插件某次 agent reload 后又开始被全量唤醒，
 * 属最难复现的一类回归）。重放同理只给匹配的 dataId；前缀**零匹配时干脆不回调**，不拿空基线打扰下游。
 * 无前缀（null/空）的订阅行为完全不变：重放全量，哪怕当前一项都没有也照常回调一次。</p>
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

    /** 过滤前缀；null/空表示不过滤（老语义）。补注册与重放都依赖它，故必须存住。 */
    private final String pathPrefix;

    /** 当前已注册的底层监听句柄；未注册时为空。 */
    private ListenerHandle handle;

    /** 已注册监听所对应的底层 EffectiveConfig 实例；用于识别 agent 换实例（reload）。 */
    private EffectiveConfig boundConfig;

    /** close 后置位，拒绝后续补注册与重放。 */
    private boolean closed;

    BeaconSubscription(ConfigChangeListener listener) {
        this(null, listener);
    }

    /** @param pathPrefix 过滤前缀（null/空=不过滤），随句柄保存，补注册与重放都按它裁剪 */
    BeaconSubscription(String pathPrefix, ConfigChangeListener listener) {
        this.listener = listener;
        this.pathPrefix = pathPrefix;
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
        handle = registerTo(config);
        boundConfig = config;
        replayCurrent(agent, config);
    }

    /**
     * 向底层 {@link EffectiveConfig} 注册（**每次都带上本句柄的前缀**，补注册不得丢）。
     *
     * <p>无前缀时坚持走单参 {@code onChange(listener)}：那是老契约，任何版本的 agent 都有；
     * 新编译的插件若跑到旧 agent jar 上，走双参重载会抛 {@code NoSuchMethodError}。
     * 有前缀时才用双参——此时过滤能力本就是刚需，agent 太旧没这个能力也不该假装订阅成功。
     * （双参方法本身在 agent-api 里有 default 降级实现，见 {@link EffectiveConfig}。）</p>
     */
    private ListenerHandle registerTo(EffectiveConfig config) {
        return DataIdPrefix.isFiltering(pathPrefix)
                ? config.onChange(pathPrefix, listener)
                : config.onChange(listener);
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

    /**
     * 用当前有效配置向监听器重放一次，使订阅方拿到注册时的基线。
     *
     * <p>重放的过滤语义必须与底层派发**一致**，否则「注册即重放」会成为绕过过滤的后门：
     * 订阅了 {@code plugins/Foo/} 的插件在注册那一刻反而收到全量 dataId，等于白过滤。</p>
     * <ul>
     *   <li>无前缀：重放当前**全部** dataId（老行为不变）；整体 md5 尚未收敛时用空串占位，
     *       仍重放一次让下游知道「已订阅、当前为空」。</li>
     *   <li>有前缀：只重放匹配项；**零匹配则整次跳过**——没有基线可给，就不该拿一次无关回调
     *       把下游叫醒。</li>
     * </ul>
     */
    private void replayCurrent(BeaconAgent agent, EffectiveConfig config) {
        List<String> dataIds = config.dataIds();
        String md5 = agent.effectiveMd5().orElse("");
        Set<String> changed;
        if (DataIdPrefix.isFiltering(pathPrefix)) {
            changed = DataIdPrefix.filter(pathPrefix, dataIds);
            if (changed.isEmpty()) {
                return;
            }
        } else {
            changed = new LinkedHashSet<>(dataIds);
        }
        listener.onConfigChanged(changed, md5);
    }
}
