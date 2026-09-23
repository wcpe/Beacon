// 告警 KPI：告警总数 + 按级别（严重 / 警告 / 提示）与按状态（待处理 / 已确认 / 已处理）计数。
// 客户端按当前页数据派生（超大量以服务端 total 明示）；级别与状态两组维度一眼看全。

import { useTranslation } from 'react-i18next'
import { Bell, CircleAlert, CircleCheck, CircleHelp, Inbox, TriangleAlert } from 'lucide-react'

import { KpiCard, type KpiTone } from '@beacon/ui'
import type { AlertEventItem } from '@beacon/contracts'

interface AlertKpiProps {
  // 记录总数（来自服务端分页 total）
  total: number
  // 当前页数据（派生级别 / 状态计数）
  items: AlertEventItem[]
}

export default function AlertKpi({ total, items }: AlertKpiProps) {
  const { t } = useTranslation()
  const countWhere = (pred: (i: AlertEventItem) => boolean) => items.filter(pred).length
  const openCount = countWhere((i) => i.status === 'open')
  const ackedCount = countWhere((i) => i.status === 'acknowledged')
  const resolvedCount = countWhere((i) => i.status === 'resolved')
  const criticalCount = countWhere((i) => i.level === 'critical')
  const warningCount = countWhere((i) => i.level === 'warning')
  const infoCount = countWhere((i) => i.level === 'info')

  // KPI 七卡：总数（品牌）/ 待处理（注意）/ 已确认（品牌）/ 已处理（正常）
  //           + 严重（危急）/ 警告（注意）/ 提示（次要）。
  const cards: { key: string; value: number; icon: typeof Bell; tone: KpiTone }[] = [
    { key: 'total', value: total, icon: Bell, tone: 'brand' },
    { key: 'open', value: openCount, icon: Inbox, tone: 'warn' },
    { key: 'acknowledged', value: ackedCount, icon: CircleHelp, tone: 'brand' },
    { key: 'resolved', value: resolvedCount, icon: CircleCheck, tone: 'ok' },
    { key: 'critical', value: criticalCount, icon: CircleAlert, tone: 'crit' },
    { key: 'warning', value: warningCount, icon: TriangleAlert, tone: 'warn' },
    { key: 'info', value: infoCount, icon: CircleHelp, tone: 'off' },
  ]

  return (
    <div className="grid gap-2.5 sm:grid-cols-2 md:grid-cols-4 xl:grid-cols-7">
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
