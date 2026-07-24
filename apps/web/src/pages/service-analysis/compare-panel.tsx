// 数据对比（多服并排矩阵）：除指标时序外，按维度并排对比所选服务器的非指标数据。
// 维度取自现有 mock：健康分 / 健康等级 / 可调度 / 不可调度原因 / 角色 / 大区 / 小区 / 摘流 / 默认入口 + 健康因子分解。
// diff 直观化：同一维度各服取值不一致的单元格标警示色；数值维度着色最优 / 最差并加 mini bar。仅聚合只读数据。

import { useMemo } from 'react'
import { useQueries, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { GitCompareArrows } from 'lucide-react'

import { AsyncSection, TableSkeleton, cn } from '@beacon/ui'
import type { HealthDetail, HealthItem, HealthLevel, ServerItem } from '@beacon/contracts'

import { fetchServers } from '../../api/cluster'
import { fetchHealthDetail, fetchHealthList } from '../../api/metrics'

interface ComparePanelProps {
  // 已选 serverId（顺序稳定）
  serverIds: string[]
}

// 单元格取值：字符串展示值 + 可选数值（用于最优 / 最差着色与 mini bar）
interface CellValue {
  text: string
  // 数值维度的比较数（越大越优，null 表示非数值维度）
  num: number | null
}

// 一行对比维度：维度名 + 各服取值
interface CompareRow {
  key: string
  label: string
  // 是否数值维度（决定是否标最优 / 最差 + mini bar）
  numeric: boolean
  cells: CellValue[]
}

export default function ComparePanel({ serverIds }: ComparePanelProps) {
  const { t } = useTranslation()

  // 复用 picker 的 servers 缓存（同 queryKey），拿基础属性
  const serversQuery = useQuery({
    queryKey: ['service-analysis', 'servers'],
    queryFn: () => fetchServers({ kind: 'backend', pageSize: 200 }),
  })

  // 一次拉全量健康列表（单次请求，非逐服 N+1），拿健康分 / 等级 / 可调度 / 原因
  const healthQuery = useQuery({
    queryKey: ['service-analysis', 'health-list'],
    queryFn: () => fetchHealthList({ pageSize: 200 }),
  })

  // 逐台拉健康因子分解（仅所选少量服务器，受选择数上限约束，非无界循环）
  const detailQueries = useQueries({
    queries: serverIds.map((serverId) => ({
      queryKey: ['service-analysis', 'health-detail', serverId],
      queryFn: () => fetchHealthDetail(serverId),
    })),
  })

  const isLoading = serversQuery.isLoading || healthQuery.isLoading || detailQueries.some((q) => q.isLoading)
  const isError = serversQuery.isError || healthQuery.isError
  const error = serversQuery.error ?? healthQuery.error

  const levelLabel: Record<HealthLevel, string> = {
    healthy: t('observability.serviceAnalysis.level.healthy'),
    degraded: t('observability.serviceAnalysis.level.degraded'),
    unhealthy: t('observability.serviceAnalysis.level.unhealthy'),
  }
  const dash = t('observability.serviceAnalysis.dash')

  const rows = useMemo<CompareRow[]>(() => {
    const serverMap = new Map<string, ServerItem>((serversQuery.data?.items ?? []).map((s) => [s.serverId, s]))
    const healthMap = new Map<string, HealthItem>((healthQuery.data?.items ?? []).map((h) => [h.serverId, h]))
    const detailMap = new Map<string, HealthDetail>()
    detailQueries.forEach((q) => {
      if (q.data) {
        detailMap.set(q.data.serverId, q.data)
      }
    })

    const bool = (v: boolean): string =>
      v ? t('observability.serviceAnalysis.yes') : t('observability.serviceAnalysis.no')

    // 基础维度：健康 + 属性
    const base: CompareRow[] = [
      {
        key: 'score',
        label: t('observability.serviceAnalysis.dims.score'),
        numeric: true,
        cells: serverIds.map((id) => {
          const s = healthMap.get(id)
          return { text: s ? s.score.toFixed(0) : dash, num: s ? s.score : null }
        }),
      },
      {
        key: 'level',
        label: t('observability.serviceAnalysis.dims.level'),
        numeric: false,
        cells: serverIds.map((id) => {
          const s = healthMap.get(id)
          return { text: s ? levelLabel[s.level] : dash, num: null }
        }),
      },
      {
        key: 'schedulable',
        label: t('observability.serviceAnalysis.dims.schedulable'),
        numeric: false,
        cells: serverIds.map((id) => {
          const s = healthMap.get(id)
          return { text: s ? bool(s.schedulable) : dash, num: null }
        }),
      },
      {
        key: 'reasons',
        label: t('observability.serviceAnalysis.dims.reasons'),
        numeric: false,
        cells: serverIds.map((id) => {
          const s = healthMap.get(id)
          const text =
            s && s.reasons.length > 0
              ? s.reasons
                  .map((r) => t(`cluster.servers.schedReason.${r}`, { defaultValue: r }))
                  .join('、')
              : t('observability.serviceAnalysis.none')
          return { text: s ? text : dash, num: null }
        }),
      },
      {
        key: 'kind',
        label: t('observability.serviceAnalysis.dims.kind'),
        numeric: false,
        cells: serverIds.map((id) => ({ text: serverMap.get(id)?.kind ?? dash, num: null })),
      },
      {
        key: 'region',
        label: t('observability.serviceAnalysis.dims.region'),
        numeric: false,
        cells: serverIds.map((id) => ({ text: serverMap.get(id)?.regionName ?? dash, num: null })),
      },
      {
        key: 'zone',
        label: t('observability.serviceAnalysis.dims.zone'),
        numeric: false,
        cells: serverIds.map((id) => ({ text: serverMap.get(id)?.zoneName ?? dash, num: null })),
      },
      {
        key: 'draining',
        label: t('observability.serviceAnalysis.dims.draining'),
        numeric: false,
        cells: serverIds.map((id) => {
          const s = serverMap.get(id)
          return { text: s ? bool(s.draining) : dash, num: null }
        }),
      },
      {
        key: 'defaultEntry',
        label: t('observability.serviceAnalysis.dims.defaultEntry'),
        numeric: false,
        cells: serverIds.map((id) => {
          const s = serverMap.get(id)
          return { text: s ? bool(s.isDefaultEntry) : dash, num: null }
        }),
      },
    ]

    // 健康因子分解：以第一台可得详情的因子集合为准（同权重版本下因子集合一致）
    const factorNames = detailMap.get(serverIds.find((id) => detailMap.has(id)) ?? '')?.factors.map((f) => f.factor) ?? []
    const factorRows: CompareRow[] = factorNames.map((factor) => ({
      key: `factor:${factor}`,
      label: `${t('observability.serviceAnalysis.dims.factorPrefix')} · ${factor}`,
      numeric: true,
      cells: serverIds.map((id) => {
        const f = detailMap.get(id)?.factors.find((x) => x.factor === factor)
        if (!f?.applicable) {
          return { text: dash, num: null }
        }
        return { text: f.normalized.toFixed(0), num: f.normalized }
      }),
    }))

    return [...base, ...factorRows]
  }, [serverIds, serversQuery.data, healthQuery.data, detailQueries, levelLabel, dash, t])

  if (serverIds.length < 2) {
    return (
      <p className="rounded-xl border border-dashed border-border bg-card/60 px-4 py-8 text-sm text-ink-3">
        {t('observability.serviceAnalysis.compareOnlyOne')}
      </p>
    )
  }

  return (
    <section className="grid gap-3">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5">
        <span className="flex items-center gap-2 text-[13px] font-semibold text-ink-1">
          <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
            <GitCompareArrows className="size-[15px]" />
          </span>
          {t('observability.serviceAnalysis.compareTitle')}
        </span>
        <span className="text-xs text-ink-4">{t('observability.serviceAnalysis.compareMission')}</span>
        <div className="ml-auto flex flex-wrap items-center gap-3 text-[11px] text-ink-4">
          <LegendDot className="bg-warn-500" label={t('observability.serviceAnalysis.legendDiff')} />
          <LegendDot className="bg-ok-500" label={t('observability.serviceAnalysis.legendBest')} />
          <LegendDot className="bg-crit-500" label={t('observability.serviceAnalysis.legendWorst')} />
        </div>
      </div>

      <AsyncSection
        isLoading={isLoading}
        isError={isError}
        error={error}
        skeleton={<TableSkeleton columns={serverIds.length + 1} rows={8} />}
      >
        {rows.length === 0 ? (
          <p className="rounded-xl border border-dashed border-border bg-card/60 px-4 py-8 text-sm text-ink-3">
            {t('observability.serviceAnalysis.compareNoData')}
          </p>
        ) : (
          <div className="overflow-x-auto rounded-xl border border-border bg-card shadow-card">
            <table className="w-full border-collapse text-sm">
              <thead>
                <tr className="border-b border-border bg-surface-2/60">
                  <th className="sticky left-0 z-10 bg-surface-2/95 px-3 py-2.5 text-left text-xs font-semibold text-ink-3">
                    {t('observability.serviceAnalysis.dimension')}
                  </th>
                  {serverIds.map((id) => (
                    <th key={id} className="px-3 py-2.5 text-left font-mono text-xs font-semibold text-ink-1">
                      {id}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <CompareTableRow key={row.key} row={row} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </AsyncSection>
    </section>
  )
}

// 单行对比：计算该维度的差异态（是否全一致）、数值维度的最优 / 最差与相对高低，渲染单元格。
function CompareTableRow({ row }: { row: CompareRow }) {
  const { t } = useTranslation()

  // 差异判定：所有非占位取值是否一致（用于警示色标记）
  const present = row.cells.filter((c) => c.num !== null || c.text !== t('observability.serviceAnalysis.dash'))
  const distinct = new Set(present.map((c) => c.text))
  const hasDiff = distinct.size > 1

  // 数值维度：最优 / 最差与量程（用于着色与 mini bar 宽度）
  const nums = row.cells.map((c) => c.num).filter((n): n is number => n !== null)
  const max = nums.length > 0 ? Math.max(...nums) : 0
  const min = nums.length > 0 ? Math.min(...nums) : 0
  const range = max - min

  return (
    <tr className="border-b border-border last:border-0">
      <th
        scope="row"
        className={cn(
          'sticky left-0 z-10 bg-card px-3 py-2 text-left text-xs font-medium text-ink-2',
          hasDiff && 'text-warn-700',
        )}
      >
        <span className="flex items-center gap-1.5">
          {hasDiff && <span className="size-1.5 shrink-0 rounded-full bg-warn-500" aria-hidden />}
          {row.label}
        </span>
      </th>
      {row.cells.map((cell, idx) => {
        // 数值维度最优 / 最差着色（量程为 0 即全相等时不着色）
        const isBest = row.numeric && cell.num !== null && range > 0 && cell.num === max
        const isWorst = row.numeric && cell.num !== null && range > 0 && cell.num === min
        // 差异态下的非数值单元格用淡警示底衬托
        const diffTint = hasDiff && !row.numeric
        const barPct = row.numeric && cell.num !== null && max > 0 ? Math.max(4, (cell.num / max) * 100) : 0
        return (
          <td
            key={idx}
            className={cn(
              'px-3 py-2 align-middle text-xs',
              diffTint && 'bg-warn-50/50',
              isBest && 'bg-ok-50/60',
              isWorst && 'bg-crit-50/50',
            )}
          >
            {row.numeric && cell.num !== null ? (
              <span className="grid gap-1">
                <span
                  className={cn(
                    'tabular-nums font-medium',
                    isBest ? 'text-ok-700' : isWorst ? 'text-crit-700' : 'text-ink-1',
                  )}
                >
                  {cell.text}
                </span>
                <span className="h-1 w-full overflow-hidden rounded-full bg-secondary">
                  <span
                    className={cn(
                      'block h-full rounded-full',
                      isBest ? 'bg-ok-500' : isWorst ? 'bg-crit-500' : 'bg-brand',
                    )}
                    style={{ width: `${String(barPct)}%` }}
                  />
                </span>
              </span>
            ) : (
              <span className={cn('text-ink-1', diffTint && 'font-medium text-warn-800')}>{cell.text}</span>
            )}
          </td>
        )
      })}
    </tr>
  )
}

// 图例圆点 + 说明
function LegendDot({ className, label }: { className: string; label: string }) {
  return (
    <span className="flex items-center gap-1">
      <span className={cn('size-2 rounded-full', className)} aria-hidden />
      {label}
    </span>
  )
}
