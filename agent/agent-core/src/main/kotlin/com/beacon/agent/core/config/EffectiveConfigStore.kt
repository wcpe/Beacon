package com.beacon.agent.core.config

import java.util.concurrent.locks.ReentrantReadWriteLock
import kotlin.concurrent.read
import kotlin.concurrent.write

/**
 * 内存中「当前有效配置」存储：items + md5 + group + zone，读写锁保护。
 *
 * 无 version 代际号。读返回拷贝，避免外部改动内部状态。
 */
class EffectiveConfigStore {

    private val lock = ReentrantReadWriteLock()

    private var md5: String? = null
    private var group: String? = null
    private var zone: String? = null
    private var items: List<ConfigItem> = emptyList()

    /** 整体替换有效配置（写锁）。 */
    fun replace(result: EffectiveResult) {
        lock.write {
            this.md5 = result.md5
            this.group = result.group
            this.zone = result.zone
            // 拷贝一份，切断与入参的共享引用。
            this.items = result.items.toList()
        }
    }

    /** 当前整体 md5；尚无有效配置时为 null。 */
    fun currentMd5(): String? = lock.read { md5 }

    /** 当前大区。 */
    fun currentGroup(): String? = lock.read { group }

    /** 当前小区。 */
    fun currentZone(): String? = lock.read { zone }

    /** 列出所有 dataId（拷贝）。 */
    fun dataIds(): List<String> = lock.read { items.map { it.dataId } }

    /** 取某 dataId 的项（拷贝引用，ConfigItem 不可变）。 */
    fun item(dataId: String): ConfigItem? = lock.read { items.firstOrNull { it.dataId == dataId } }

    /** 全部项的快照拷贝。 */
    fun snapshotItems(): List<ConfigItem> = lock.read { items.toList() }
}
