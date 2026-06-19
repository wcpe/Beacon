package top.wcpe.beacon.agent.core.messaging

/**
 * 消息传输端口（ADR-0016 决策 7/14：core 只依赖本抽象，Redis 客户端关在适配器里）。
 *
 * 本接口只暴露「与内容无关的原始 json 文本搬运」原语，不理解任何信封语义——
 * 信封编解码、路由分发、RPC 配对均在 [MessageBus]（core）完成。具体实现（Redis Streams +
 * 消费组 + pub/sub + 回信通道）在 agent-adapters，core 不 import Jedis（守不变量 #5）。
 *
 * 线程约定：实现内部用独立连接/线程（与配置同步、心跳故障域隔离）。订阅回调在适配器的
 * 后台线程触发（绝不上 MC 主线程）；需碰平台 API 由上层切回。
 */
interface MessageTransport {

    /** 启动传输（连接 Redis、起订阅消费线程）。失败抛异常由上层降级。 */
    fun start()

    /** 关闭传输（停消费线程、释放连接）。幂等。 */
    fun close()

    /** 当前是否可用（已连上 Redis）。不可用时 [MessageBus] 的发送报失败、RPC 超时。 */
    fun isConnected(): Boolean

    /**
     * 定向可靠投递：把 raw json 写入目标服收件流（适配器走 Redis Streams + MAXLEN 裁剪）。
     *
     * 目标离线时消息留存于流，目标上线后经其消费组补消费（至少送达一次）。
     *
     * @param serverId 目标子服 serverId
     * @param rawJson  已编码的信封 json 文本
     */
    fun sendToServer(serverId: String, rawJson: String)

    /**
     * 主题发布（可丢，pub/sub）：当前无订阅者即丢弃，不留存。
     *
     * @param topic   主题名
     * @param rawJson 已编码的信封 json 文本
     */
    fun publishTopic(topic: String, rawJson: String)

    /**
     * 回信投递（RPC 响应）：把 raw json 写入发起方专属回信通道（适配器走 pub/sub，两端在线 + 超时兜底）。
     *
     * @param replyChannel 发起方在请求里带的 replyTo
     * @param rawJson      已编码的响应信封 json 文本
     */
    fun sendReply(replyChannel: String, rawJson: String)

    /**
     * 订阅本服收件流（消费组 = 本服 serverId），逐条把 raw json 回调给 [onMessage]。
     *
     * 实现须在重连后从上次未 ack 位置补消费（至少送达一次，业务侧幂等）。本方法仅注册回调，
     * 实际消费在 [start] 起的后台线程。
     */
    fun subscribeServerInbox(onMessage: (rawJson: String) -> Unit)

    /**
     * 订阅本服专属回信通道，逐条把回信 raw json 回调给 [onMessage]（供 RPC 唤醒等待的 Future）。
     */
    fun subscribeReplyInbox(replyChannel: String, onMessage: (rawJson: String) -> Unit)

    /** 订阅一个主题；同一 topic 多次订阅由适配器去重。 */
    fun subscribeTopic(topic: String, onMessage: (rawJson: String) -> Unit)

    /** 取消订阅一个主题。 */
    fun unsubscribeTopic(topic: String)
}
