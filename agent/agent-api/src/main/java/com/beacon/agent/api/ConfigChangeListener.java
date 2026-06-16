package com.beacon.agent.api;

import java.util.Set;

/**
 * 有效配置变更回调。
 *
 * <p>注意：回调在 agent 异步线程触发，重活请自行切到业务线程。</p>
 */
public interface ConfigChangeListener {

    /**
     * @param changedDataIds 本次变更的 dataId 集合
     * @param newMd5         变更后整体 md5（小写 hex）
     */
    void onConfigChanged(Set<String> changedDataIds, String newMd5);
}
