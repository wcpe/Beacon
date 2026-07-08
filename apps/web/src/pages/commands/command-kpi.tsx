// 命令观测 KPI：命令总数 + 按状态计数（待取走 / 已完成 / 失败 / 过期），取自 commands/analytics。
// 对齐 B 版：图标角标 KpiCard 卡带，按状态语义上色。

import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { CircleCheck, CircleX, Clock, Hourglass, Terminal } from 'lucide-react'

import { AsyncSection, CardGridSkeleton, KpiCard, type KpiTone } from '@beacon/ui'

import { fetchCommandAnalytics } from '../../api/observability'

// 单个 KPI 定义（图标 + 色调 + 取数 status，total 为 null）。
interface KpiDef {
  key: string
  status: string | null
  icon: typeof Terminal
  tone: KpiTone
}

const KPIS: KpiDef[] = [
  { key: 'total', status: null, icon: Terminal, tone: 'brand' },
  { key: 'pending', status: 'pending', icon: Hourglass, tone: 'warn' },
  { key: 'done', status: 'done', icon: CircleCheck, tone: 'ok' },
  { key: 'failed', status: 'failed', icon: CircleX, tone: 'crit' },
  { key: 'expired', status: 'expired', icon: Clock, tone: 'off' },
]

export default function CommandKpi() {
  const { t } = useTranslation()
  const query = useQuery({ queryKey: ['commands', 'analytics'], queryFn: fetchCommandAnalytics })

  const values = useMemo(() => {
    const data = query.data
    const countOf = (status: string) => data?.byStatus.find((s) => s.status === status)?.count ?? 0
    return KPIS.map((k) => (k.status === null ? (data?.total ?? 0) : countOf(k.status)))
  }, [query.data])

  return (
    <AsyncSection
      isLoading={query.isLoading}
      isError={query.isError}
      error={query.error}
      skeleton={<CardGridSkeleton count={5} />}
    >
      <div className="grid gap-3.5 sm:grid-cols-2 xl:grid-cols-5">
        {KPIS.map((k, i) => {
          const Icon = k.icon
          return (
            <KpiCard
              key={k.key}
              label={t(`observability.commands.kpi.${k.key}`)}
              value={values[i]}
              icon={<Icon className="size-4" />}
              tone={k.tone}
            />
          )
        })}
      </div>
    </AsyncSection>
  )
}
