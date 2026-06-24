// 控制面连接状态 hook（FR-78）。
// 管理台（浏览器侧）无面向前端的 SSE——唯一 SSE 是 agent↔控制面，不面向浏览器（见
// docs/specs/connection-status-indicator.md §3.1）。故连通态从既有 system-status 轮询查询
// 的成功 / 失败派生：SystemHeader 已每 5s 轮询 /admin/v1/system/status 且常驻挂载，天然是
// 全局心跳。本 hook 只读其状态、不自建轮询（避免重复请求与新传输通道）。
//
// 自动重连：react-query 轮询在控制面恢复后下一周期自然成功，状态由 offline 翻回 online；
// 本 hook 监听该「offline → online」边沿，触发一次全量 invalidateQueries 使各页面立即重取
// 最新数据，免去运维手动刷新。

import { useEffect, useRef } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { systemStatus } from '@/api/client'

// 心跳查询键：与 SystemHeader 的 useQuery 一致。react-query 按 key 去重——相同 key 共享同一份
// 拉取与缓存，本 hook 复用既有 5s 轮询，不会多发请求。
const HEARTBEAT_QUERY_KEY = ['system-status'] as const
// 心跳轮询周期（毫秒）：与 SystemHeader 一致；同 key 多观察者取最短周期，行为不变。
const HEARTBEAT_REFETCH_MS = 5000

// 连接态：connecting=首次请求在途（未出错，不弹断开）；online=最近一次成功；offline=最近一次失败。
export type ConnectionStatus = 'connecting' | 'online' | 'offline'

export interface ConnectionState {
  status: ConnectionStatus
}

// 订阅 system-status 心跳查询、派生连通态。
// 用 useQuery（而非只读缓存）以保证心跳状态变化时本组件重渲染；同 key 与 SystemHeader 共享轮询。
export function useConnectionStatus(): ConnectionState {
  const queryClient = useQueryClient()
  const { status: queryStatus } = useQuery({
    queryKey: HEARTBEAT_QUERY_KEY,
    queryFn: systemStatus,
    refetchInterval: HEARTBEAT_REFETCH_MS,
  })
  const status = deriveStatus(queryStatus)

  // 记录上一次的连通态，用于识别「offline → online」恢复边沿。
  const prevStatusRef = useRef<ConnectionStatus>(status)
  useEffect(() => {
    if (prevStatusRef.current === 'offline' && status === 'online') {
      // 控制面恢复：失效全部查询，触发各页面重取最新数据（自动刷新）。
      void queryClient.invalidateQueries()
    }
    prevStatusRef.current = status
  }, [status, queryClient])

  return { status }
}

// 由 react-query 查询的 status 映射为连通态。
// - 查询出错（status='error'）→ offline（含网络断开、控制面进程未起）。
// - 查询有成功结果（status='success'）→ online。
// - 其余（pending 且未出错）→ connecting：首屏请求在途，不误判断开。
function deriveStatus(queryStatus: string | undefined): ConnectionStatus {
  if (queryStatus === 'error') return 'offline'
  if (queryStatus === 'success') return 'online'
  return 'connecting'
}
