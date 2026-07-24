// 进度时间线 Tab：GET events → 共享双模式时间线（可视化垂直时间轴 / 详细表格）。
import { useQuery } from '@tanstack/react-query'

import { AsyncSection } from '@beacon/ui'

import { fetchChangeEvents } from '../../api/delivery-changes'
import EventsTimeline from '../../features/delivery/events-timeline'

interface EventsTabProps {
  orderId: number
}

export default function EventsTab({ orderId }: EventsTabProps) {
  // 事件用一次性 fetch（契约为轮询替代 SSE），保持 5s 刷新但测试不依赖轮询。
  const query = useQuery({
    queryKey: ['change-orders', 'events', orderId],
    queryFn: () => fetchChangeEvents(orderId),
    refetchInterval: 5000,
  })

  return (
    <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
      <EventsTimeline events={query.data?.events ?? []} />
    </AsyncSection>
  )
}
