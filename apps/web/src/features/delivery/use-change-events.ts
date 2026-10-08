// 变更单进度事件订阅（FR-256）：优先 SSE 实时推送，断流 / 不可用回退 5s 轮询。
//
// 契约：GET /admin/v2/change-orders/{id}/events 按 Accept 内容协商——
// text/event-stream → SSE 流；否则一次性事件数组。两者事件同形（ChangeOrderEvent）。
// 快照与实时流按 seq 去重合并，避免断线重连时重复或丢进度。
import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'

import {
  fetchChangeEvents,
  subscribeChangeEvents,
  type ChangeOrderEvent,
} from '../../api/delivery-changes'

/** 断线回退轮询间隔（秒级：与进度推进节奏匹配，不额外加重控制面） */
export const EVENTS_FALLBACK_POLL_MS = 5000

export interface ChangeEventsState {
  events: ChangeOrderEvent[]
  isLoading: boolean
  isError: boolean
  error: unknown
  /** true = SSE 实时推送中；false = 已回退 5s 轮询 */
  live: boolean
}

export function useChangeEvents(orderId: number): ChangeEventsState {
  // SSE 实时事件：只累积本次订阅收到的（切换单据即清空）
  const [streamEvents, setStreamEvents] = useState<ChangeOrderEvent[]>([])
  const [live, setLive] = useState(false)

  const query = useQuery({
    queryKey: ['change-orders', 'events', orderId],
    queryFn: () => fetchChangeEvents(orderId),
    // 实时推送期间不轮询；断流（live=false）即按 5s 回退
    refetchInterval: live ? false : EVENTS_FALLBACK_POLL_MS,
  })

  useEffect(() => {
    setStreamEvents([])
    setLive(false)
    return subscribeChangeEvents(orderId, {
      onOpen: () => {
        setLive(true)
      },
      onEvent: (event) => {
        setStreamEvents((prev) => mergeEvent(prev, event))
      },
      // 失败或流结束都按断流处理：回退轮询（onClose 不带错误即服务端正常关流）
      onClose: () => {
        setLive(false)
      },
    })
  }, [orderId])

  const events = useMemo(
    () => mergeEvents(query.data?.events ?? [], streamEvents),
    [query.data, streamEvents],
  )

  return {
    events,
    isLoading: query.isLoading,
    // 已经拿到实时事件就不算错误：控制面关流 / 轮询偶发失败都不该清掉已看到的进度
    isError: query.isError && query.data === undefined && streamEvents.length === 0,
    error: query.error,
    live,
  }
}

// 按 seq 去重追加（同号事件只保留一条）
function mergeEvent(list: ChangeOrderEvent[], event: ChangeOrderEvent): ChangeOrderEvent[] {
  if (list.some((item) => item.seq === event.seq)) {
    return list
  }
  return [...list, event].sort((left, right) => left.seq - right.seq)
}

// 快照 ∪ 实时事件：实时事件优先（同 seq 以快照为准即可，两者同形）
function mergeEvents(snapshot: ChangeOrderEvent[], streamed: ChangeOrderEvent[]): ChangeOrderEvent[] {
  return streamed.reduce(mergeEvent, snapshot)
}
