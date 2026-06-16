package com.beacon.agent.api;

/** 监听器注销句柄。调用 {@link #remove()} 后不再收到回调。 */
public interface ListenerHandle {

    /** 注销监听器。重复调用安全。 */
    void remove();
}
