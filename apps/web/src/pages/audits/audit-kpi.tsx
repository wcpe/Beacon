// 审计 KPI：审计总数 + 成功 / 失败 / 成功率 + 动作种类 / 最高频动作，取自 audits/analytics。
// 前四张给规模与结果，后两张给「审计面有多广、最常发生什么」。

import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Activity, CircleCheck, CircleX, ListTree, ScrollText, TrendingUp } from 'lucide-react'

import { AsyncSection, CardGridSkeleton, KpiCard, type KpiTone } from '@beacon/ui'

import { fetchAuditAnalytics } from '../../api/observability'

export default function AuditKpi() {
  const { t } = useTranslation()
  const query = useQuery({ queryKey: ['audits', 'analytics'], queryFn: fetchAuditAnalytics })

  // KPI 六卡：总数（品牌）/ 成功（正常）/ 失败（危急）/ 成功率 / 动作种类 / 最高频动作。
  const cards = useMemo<
    { key: string; value: string | number; icon: typeof ScrollText; tone: KpiTone; meta?: string }[]
  >(() => {
    const data = query.data
    const ok = data?.okCount ?? 0
    const fail = data?.failCount ?? 0
    const total = data?.total ?? 0
    const byAction = data?.byAction ?? []
    // 成功率：无记录时以「—」表示不可计算，不显示误导性的 0%
    const okRate = total === 0 ? '—' : `${((ok / total) * 100).toFixed(1)}%`
    // 最高频动作：按 count 降序取首项；动作在表格中显示为原始 key，故这里直接透传并保留计数
    const top = byAction.length === 0 ? null : byAction.reduce((a, b) => (b.count > a.count ? b : a))
    return [
      { key: 'total', value: total, icon: ScrollText, tone: 'brand' },
      { key: 'ok', value: ok, icon: CircleCheck, tone: 'ok' },
      { key: 'fail', value: fail, icon: CircleX, tone: fail > 0 ? 'crit' : 'off' },
      { key: 'okRate', value: okRate, icon: TrendingUp, tone: total === 0 ? 'off' : 'brand' },
      { key: 'actionKinds', value: byAction.length, icon: ListTree, tone: byAction.length > 0 ? 'brand' : 'off' },
      {
        key: 'topAction',
        value: top?.action ?? '—',
        icon: Activity,
        tone: top ? 'brand' : 'off',
        meta: top ? `×${String(top.count)}` : undefined,
      },
    ]
  }, [query.data])

  return (
    <AsyncSection
      isLoading={query.isLoading}
      isError={query.isError}
      error={query.error}
      skeleton={<CardGridSkeleton count={6} />}
    >
      <div className="grid gap-2.5 sm:grid-cols-2 md:grid-cols-3 xl:grid-cols-6">
        {cards.map((c) => {
          const Icon = c.icon
          return (
            <KpiCard
              key={c.key}
              label={t(`observability.audits.kpi.${c.key}`)}
              value={c.value}
              icon={<Icon className="size-4" />}
              tone={c.tone}
              meta={c.meta}
            />
          )
        })}
      </div>
    </AsyncSection>
  )
}
