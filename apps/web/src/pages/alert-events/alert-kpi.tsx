// 告警 KPI：告警总数 + 待处理 / 严重 / 已处理计数（客户端按当前页数据派生，超大量以 total 明示）。

import { useTranslation } from 'react-i18next'

import { SummaryStrip, type SummaryItem } from '@beacon/ui'
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

  const summary: SummaryItem[] = [
    { label: t('observability.alertEvents.kpi.total'), value: total },
    { label: t('observability.alertEvents.kpi.open'), value: openCount, tone: 'warning' },
    { label: t('observability.alertEvents.kpi.critical'), value: criticalCount, tone: 'danger' },
    { label: t('observability.alertEvents.kpi.resolved'), value: resolvedCount, tone: 'success' },
  ]
  return <SummaryStrip items={summary} />
}
