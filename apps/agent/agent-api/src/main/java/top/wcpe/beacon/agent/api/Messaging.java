package top.wcpe.beacon.agent.api;

import java.util.concurrent.CompletableFuture;

/**
 * 跨服消息中间件门面（FR-26 / ADR-0016）：对③层业务插件暴露与内容无关的通用传输。
 *
 * <p>四种模式：定向发送、请求-响应（RPC）、主题发布订阅、按玩家所在服寻址；外加按类型收消息。
 * 中间件只搬运、不理解业务语义；匹配 / 对战 / 存储 / 排行等游戏功能由业务插件自行实现。</p>
 *
 * <p><b>软依赖 + 降级</b>：模块未开启或 Redis 未连上时 {@link #isAvailable()} 为 false，
 * 发送类方法抛 {@link IllegalStateException}；业务插件应先判 {@link #isAvailable()} 再用，优雅降级。</p>
 *
 * <p><b>线程</b>：发送类方法可在任意线程调用（内部仅编码 + 投递）。收到消息的 handler 在中间件后台线程
 * 触发（<b>绝不在 MC 主线程</b>）；handler 若要碰 Bukkit/Bungee API，需自行切回平台线程。</p>
 *
 * <p>payload 为「泛型树」：{@code Map<String,Object>} / {@code List<Object>} / 字符串 / 数字 / 布尔 / null，
 * 经 JSON 往返。演进遵循「只增不改」（可加可选字段，不删/不改已有字段）。</p>
 */
public interface Messaging {

    /** 模块是否可用（已启用且 Redis 已连上）。业务侧据此优雅降级。 */
    boolean isAvailable();

    /**
     * 定向发送（fire-and-forget，可靠送达）：写入目标服收件流，目标离线则上线后补收。
     *
     * @param targetServerId 目标子服 serverId
     * @param type           业务消息类型（目标按 {@link #on} 注册的同名处理器分发）
     * @param payload        业务负载（泛型树）
     * @throws IllegalStateException 模块不可用
     */
    void send(String targetServerId, String type, Object payload);

    /**
     * 请求-响应（RPC）：发请求并立即返回 Future，目标回信后完成；超时则 Future 异常完成
     * （{@link java.util.concurrent.TimeoutException}）。
     *
     * @return 完成值为目标返回的 payload（泛型树）
     * @throws IllegalStateException 模块不可用
     */
    CompletableFuture<Object> call(String targetServerId, String type, Object payload);

    /**
     * 主题发布（可丢广播，FR-180 / ADR-0065）：向发送者 namespace 当前全部在线服 fan-out 一条广播
     * （含发送者自身），topic 落 msg_type，接收方按 {@link #subscribe} 注册的同名处理器分发。
     *
     * <p>可丢语义：只投当前在线服，离线不补投；要可靠送达请用 {@link #send} / {@link #call}。
     * 广播不跨 namespace，不出信任边界。</p>
     *
     * @param topic   主题名（落 msg_type，接收方据此路由订阅处理器）
     * @param payload 业务负载（泛型树）
     * @throws IllegalStateException 模块不可用
     */
    void publish(String topic, Object payload);

    /**
     * 主题发布 · zone 级定向（FR-180 重载，接口只增不改、守二进制兼容）：只向发送者 namespace 内指定
     * {@code zone} 的当前在线服 fan-out；其余语义同 {@link #publish(String, Object)}（可丢、不补投、不出信任边界）。
     *
     * <p>默认实现忽略 {@code zone} 退化为全 namespace 广播（保证既有实现零改动仍可编译）；
     * core 门面已覆盖此方法、按 {@code zone} 定向。</p>
     *
     * @param topic   主题名
     * @param payload 业务负载（泛型树）
     * @param zone    目标 zone；{@code null} 等价于 {@link #publish(String, Object)}（全 namespace 广播）
     * @throws IllegalStateException 模块不可用
     */
    default void publish(String topic, Object payload, String zone) {
        publish(topic, payload);
    }

    /**
     * 主题订阅（FR-180 / ADR-0065）：登记本地按 topic 分发的处理器，接收经 {@link #publish} 广播到本服的该主题消息。
     * 与 {@link #on} 的定向消息分发表相互隔离、互不干扰。
     *
     * @return 可注销句柄（{@link ListenerHandle#remove()} 后取消订阅）
     * @throws IllegalStateException 模块不可用
     */
    ListenerHandle subscribe(String topic, TopicHandler handler);

    /**
     * 按玩家寻址：解析玩家当前所在服后定向发送（依赖 beacon-proxy 维护的玩家名册）。
     *
     * @return true=已解析并投递；false=名册无此玩家（找不到目标兜底，调用方可重试 / 丢弃）
     * @throws IllegalStateException 模块不可用 / 未配置玩家位置解析
     */
    boolean sendToPlayer(String playerName, String type, Object payload);

    /**
     * 注册按消息类型分发的处理器（收消息入口）。同 type 重复注册以最后一次为准。
     *
     * <p>若收到的是 RPC 请求，可在处理器内经 {@link IncomingMessage#reply(Object)} 回信。</p>
     *
     * @return 可注销句柄
     */
    ListenerHandle on(String type, MessageHandler handler);
}
