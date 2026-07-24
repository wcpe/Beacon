package top.wcpe.beacon.agent.api;

/**
 * Beacon agent 对外门面的获取入口。
 *
 * <p>业务插件通过 {@link #get()} 拿到 {@link BeaconAgent}，进而读「有效配置」与查「服务发现」。
 * 实现由 agent-bukkit / agent-bungee 在插件 ENABLE 阶段注入，DISABLE 阶段注销。</p>
 */
public final class BeaconAgentProvider {

    /** 当前门面实例；volatile 保证跨线程可见性。 */
    private static volatile BeaconAgent instance;

    private BeaconAgentProvider() {
    }

    /**
     * 获取 Beacon agent 门面。
     *
     * @return 已就绪的门面
     * @throws AgentUnavailableException agent 尚未初始化或已卸载
     */
    public static BeaconAgent get() {
        BeaconAgent ref = instance;
        if (ref == null) {
            throw new AgentUnavailableException("Beacon agent 尚未初始化或已卸载");
        }
        return ref;
    }

    /** agent 是否已就绪（业务插件可据此先行判断，避免触发异常）。 */
    public static boolean isAvailable() {
        return instance != null;
    }

    /** 由 agent 实现层在 ENABLE 阶段注册；业务插件不应调用。 */
    public static void register(BeaconAgent agent) {
        instance = agent;
    }

    /** 由 agent 实现层在 DISABLE 阶段注销；业务插件不应调用。 */
    public static void unregister() {
        instance = null;
    }
}
