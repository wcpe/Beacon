// 审计 KPI：审计总数 + 成功 / 失败计数，取自 audits/analytics。

import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { AsyncSection, SummaryStrip, TileGridSkeleton, type SummaryItem } from '@beacon/ui'

import { fetchAuditAnalytics } from '../../api/observability'

export default function AuditKpi() {
  const { t } = useTranslation()
  const query = useQuery({ queryKey: ['audits', 'analytics'], queryFn: fetchAuditAnalytics })

  const items = useMemo<SummaryItem[]>(() => {
    const data = query.data
    return [
      { label: t('observability.audits.kpi.total'), value: data?.total ?? 0 },
      { label: t('observability.audits.kpi.ok'), value: data?.okCount ?? 0, tone: 'success' },
      { label: t('observability.audits.kpi.fail'), value: data?.failCount ?? 0, tone: 'danger' },
    ]
  }, [query.data, t])

  return (
    <AsyncSection
      isLoading={query.isLoading}
      isError={query.isError}
      error={query.error}
      skeleton={<TileGridSkeleton count={3} />}
    >
      <SummaryStrip items={items} />
    </AsyncSection>
  )
}
