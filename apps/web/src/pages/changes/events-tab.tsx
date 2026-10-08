// 进度时间线 Tab：GET .../events（SSE 实时推送，断流回退 5s 轮询）→ 共享双模式时间线。
import { useTranslation } from 'react-i18next'

import { Activity } from 'lucide-react'

import { AsyncSection } from '@beacon/ui'

import EventsTimeline from '../../features/delivery/events-timeline'
import { useChangeEvents } from '../../features/delivery/use-change-events'

interface EventsTabProps {
  orderId: number
}

export default function EventsTab({ orderId }: EventsTabProps) {
  const { t } = useTranslation()
  const { events, isLoading, isError, error, live } = useChangeEvents(orderId)

  return (
    <AsyncSection isLoading={isLoading} isError={isError} error={error}>
      <div className="grid gap-2">
        {/* 实时性状态：SSE 连上 = 实时推送；断线回退 5s 轮询（不让用户误以为进度停住） */}
        <p className="flex items-center gap-1.5 text-xs text-ink-3">
          <Activity className="size-3.5" aria-hidden />
          {live ? t('delivery.changes.detail.events.live') : t('delivery.changes.detail.events.polling')}
        </p>
        <EventsTimeline events={events} />
      </div>
    </AsyncSection>
  )
}
