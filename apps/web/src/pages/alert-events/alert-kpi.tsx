// 告警 KPI：告警总数 + 待处理 / 严重 / 已处理计数（客户端按当前页数据派生，超大量以 total 明示）。
// 对齐 B 版：图标角标 KpiCard 卡带，按语义上色。

import { useTranslation } from 'react-i18next'
import { Bell, CircleAlert, CircleCheck, Inbox } from 'lucide-react'

import { KpiCard, type KpiTone } from '@beacon/ui'
import type { AlertEventItem } from '@beacon/devmock'

interface AlertKpiProps {
  // 记录总数（来自服务端分页 total）
  total: number
  // 当前页数据（派生级别 / 状态计数）
  items: AlertEventItem[]
}

export default function AlertKpi({ total, items }: AlertKpiProps) {
  const { t } = useTranslation()
  const openCount = items.filter((i) => i.status === 'open').length
  const criticalCount = items.filter((i) => i.level === 'critical').length
  const resolvedCount = items.filter((i) => i.status === 'resolved').length

  // KPI 四卡：总数（品牌）/ 待处理（注意）/ 严重（危急）/ 已处理（正常）。
  const cards: { key: string; value: number; icon: typeof Bell; tone: KpiTone }[] = [
    { key: 'total', value: total, icon: Bell, tone: 'brand' },
    { key: 'open', value: openCount, icon: Inbox, tone: 'warn' },
    { key: 'critical', value: criticalCount, icon: CircleAlert, tone: 'crit' },
    { key: 'resolved', value: resolvedCount, icon: CircleCheck, tone: 'ok' },
  ]

  return (
    <div className="grid gap-3.5 sm:grid-cols-2 xl:grid-cols-4">
      {cards.map((c) => {
        const Icon = c.icon
        return (
          <KpiCard
            key={c.key}
            label={t(`observability.alertEvents.kpi.${c.key}`)}
            value={c.value}
            icon={<Icon className="size-4" />}
            tone={c.tone}
          />
        )
      })}
    </div>
  )
}
