// 命令观测 KPI：命令总数 + 按状态计数（待取走 / 已完成 / 失败 / 过期）+ 命令类型数 / 涉及服务器数，
// 取自 commands/analytics。前五张给吞吐与结果，后两张给覆盖面。

import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  CircleCheck,
  CircleX,
  Clock,
  Hourglass,
  Layers,
  Server,
  Terminal,
} from 'lucide-react'

import { AsyncSection, CardGridSkeleton, KpiCard, type KpiTone } from '@beacon/ui'

import { fetchCommandAnalytics } from '../../api/observability'

// 单个 KPI 定义（图标 + 色调 + 取数 key；total 走服务端总量，其余按 status / 派生）。
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
  // 后两张为派生量：命令类型种类数、涉及服务器数（各取 byType / byServer 的长度）
  { key: 'typeKinds', status: null, icon: Layers, tone: 'brand' },
  { key: 'servers', status: null, icon: Server, tone: 'brand' },
]

export default function CommandKpi() {
  const { t } = useTranslation()
  const query = useQuery({ queryKey: ['commands', 'analytics'], queryFn: fetchCommandAnalytics })

  const values = useMemo(() => {
    const data = query.data
    const countOf = (status: string) => data?.byStatus.find((s) => s.status === status)?.count ?? 0
    return KPIS.map((k) => {
      switch (k.key) {
        case 'total':
          return data?.total ?? 0
        case 'typeKinds':
          return data?.byType.length ?? 0
        case 'servers':
          return data?.byServer.length ?? 0
        default:
          return countOf(k.status ?? '')
      }
    })
  }, [query.data])

  return (
    <AsyncSection
      isLoading={query.isLoading}
      isError={query.isError}
      error={query.error}
      skeleton={<CardGridSkeleton count={7} />}
    >
      <div className="grid gap-2.5 sm:grid-cols-2 md:grid-cols-4 xl:grid-cols-7">
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
