package top.wcpe.beacon.agent.api;

/** 收到的一条跨服消息（按类型分发给 {@link MessageHandler}）。 */
public interface IncomingMessage {

    /** 业务消息类型。 */
    String type();

    /** 业务负载（泛型树：Map/List/字符串/数字/布尔/null）。 */
    Object payload();

    /** 发起方 serverId（可能为 null）。 */
    String source();

    /** 本消息是否为 RPC 请求（可经 {@link #reply(Object)} 回信）。 */
    boolean isRequest();

    /**
     * 回信（仅 RPC 请求有效；非请求调用无副作用）。
     *
     * @param payload 响应负载（泛型树）
     */
    void reply(Object payload);
}
