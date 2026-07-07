// 命令观测 KPI：命令总数 + 按状态计数（待取走 / 已完成 / 失败 / 过期），取自 commands/analytics。

import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { AsyncSection, SummaryStrip, TileGridSkeleton, type SummaryItem } from '@beacon/ui'

import { fetchCommandAnalytics } from '../../api/observability'

export default function CommandKpi() {
  const { t } = useTranslation()
  const query = useQuery({ queryKey: ['commands', 'analytics'], queryFn: fetchCommandAnalytics })

  const items = useMemo<SummaryItem[]>(() => {
    const data = query.data
    const countOf = (status: string) => data?.byStatus.find((s) => s.status === status)?.count ?? 0
    return [
      { label: t('observability.commands.kpi.total'), value: data?.total ?? 0 },
      { label: t('observability.commands.kpi.pending'), value: countOf('pending'), tone: 'warning' },
      { label: t('observability.commands.kpi.done'), value: countOf('done'), tone: 'success' },
      { label: t('observability.commands.kpi.failed'), value: countOf('failed'), tone: 'danger' },
      { label: t('observability.commands.kpi.expired'), value: countOf('expired'), tone: 'muted' },
    ]
  }, [query.data, t])

  return (
    <AsyncSection
      isLoading={query.isLoading}
      isError={query.isError}
      error={query.error}
      skeleton={<TileGridSkeleton count={5} />}
    >
      <SummaryStrip items={items} />
    </AsyncSection>
  )
}
