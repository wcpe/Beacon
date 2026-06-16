package com.beacon.agent.core.config

/**
 * 一次有效配置拉取结果（对应 config/effective 200 响应）。
 *
 * 无 version 代际号——收敛只看 md5。zone 未分配时为 null。
 *
 * @param namespace 环境
 * @param serverId  本机身份
 * @param group     控制面解析的大区
 * @param zone      控制面指派的小区（未指派为 null）
 * @param md5       整体 md5（小写 hex）
 * @param items     合并后的有效配置项列表
 */
data class EffectiveResult(
    val namespace: String,
    val serverId: String,
    val group: String?,
    val zone: String?,
    val md5: String,
    val items: List<ConfigItem>,
)
