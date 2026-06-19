package top.wcpe.beacon.agent.api;

/** 主题消息处理器（函数式接口）。在中间件后台线程触发，绝不在 MC 主线程。 */
@FunctionalInterface
public interface TopicHandler {

    /**
     * 处理一条主题消息。
     *
     * @param topic   主题名
     * @param payload 业务负载（泛型树）
     */
    void handle(String topic, Object payload);
}
