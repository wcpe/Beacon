package top.wcpe.beacon.agent.api;

/** 按类型分发的消息处理器（函数式接口）。在中间件后台线程触发，绝不在 MC 主线程。 */
@FunctionalInterface
public interface MessageHandler {

    /** 处理一条收到的消息。 */
    void handle(IncomingMessage message);
}
