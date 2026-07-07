package top.wcpe.beacon.agent.api;

/**
 * agent 门面尚未就绪（未初始化或已卸载）时抛出。
 *
 * <p>业务插件应捕获本异常并降级（如使用内置默认配置），而非令自身启动失败。</p>
 */
public class AgentUnavailableException extends RuntimeException {

    private static final long serialVersionUID = 1L;

    public AgentUnavailableException(String message) {
        super(message);
    }
}
