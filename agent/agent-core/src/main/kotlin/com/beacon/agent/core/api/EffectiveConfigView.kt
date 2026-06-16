package com.beacon.agent.core.api

import com.beacon.agent.api.ConfigChangeListener
import com.beacon.agent.api.EffectiveConfig
import com.beacon.agent.api.ListenerHandle
import com.beacon.agent.core.config.EffectiveConfigStore
import java.util.Optional
import java.util.concurrent.ConcurrentHashMap

/**
 * EffectiveConfig 的 core 实现：读取来自 EffectiveConfigStore，变更监听由 apply 流程经
 * {@link #fireChanged} 派发。两个平台壳共用，避免重复。
 */
class EffectiveConfigView(
    private val store: EffectiveConfigStore,
) : EffectiveConfig {

    // 监听器表：以句柄对象自身为键，便于注销。
    private val listeners = ConcurrentHashMap<Any, ConfigChangeListener>()

    override fun dataIds(): List<String> = store.dataIds()

    override fun raw(dataId: String): Optional<String> =
        Optional.ofNullable(store.item(dataId)?.content)

    override fun format(dataId: String): Optional<String> =
        Optional.ofNullable(store.item(dataId)?.format)

    override fun md5(dataId: String): Optional<String> =
        Optional.ofNullable(store.item(dataId)?.md5)

    override fun onChange(listener: ConfigChangeListener): ListenerHandle {
        val key = Any()
        listeners[key] = listener
        return ListenerHandle { listeners.remove(key) }
    }

    /** 由 apply 流程在有效配置变更后调用，回调所有监听器。 */
    fun fireChanged(changedDataIds: Set<String>, newMd5: String) {
        for (listener in listeners.values) {
            listener.onConfigChanged(changedDataIds, newMd5)
        }
    }
}
