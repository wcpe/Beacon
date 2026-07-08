// 审计 KPI：审计总数 + 成功 / 失败计数，取自 audits/analytics。
// 对齐 B 版：图标角标 KpiCard 卡带，按结果语义上色。

import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { CircleCheck, CircleX, ScrollText } from 'lucide-react'

import { AsyncSection, CardGridSkeleton, KpiCard, type KpiTone } from '@beacon/ui'

import { fetchAuditAnalytics } from '../../api/observability'

export default function AuditKpi() {
  const { t } = useTranslation()
  const query = useQuery({ queryKey: ['audits', 'analytics'], queryFn: fetchAuditAnalytics })

  // KPI 三卡：总数（品牌）/ 成功（绿）/ 失败（红）。
  const cards = useMemo<{ key: string; value: number; icon: typeof ScrollText; tone: KpiTone }[]>(() => {
    const data = query.data
    return [
      { key: 'total', value: data?.total ?? 0, icon: ScrollText, tone: 'brand' },
      { key: 'ok', value: data?.okCount ?? 0, icon: CircleCheck, tone: 'ok' },
      { key: 'fail', value: data?.failCount ?? 0, icon: CircleX, tone: 'crit' },
    ]
  }, [query.data])

  return (
    <AsyncSection
      isLoading={query.isLoading}
      isError={query.isError}
      error={query.error}
      skeleton={<CardGridSkeleton count={3} />}
    >
      <div className="grid gap-3.5 sm:grid-cols-3">
        {cards.map((c) => {
          const Icon = c.icon
          return (
            <KpiCard
              key={c.key}
              label={t(`observability.audits.kpi.${c.key}`)}
              value={c.value}
              icon={<Icon className="size-4" />}
              tone={c.tone}
            />
          )
        })}
      </div>
    </AsyncSection>
  )
}
