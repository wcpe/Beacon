// 健康快照回放（/service-analysis 板块）：对所选每台服务器回放时间窗内健康分 / 等级随时间的变化
// （分数 sparkline + 最新 / 最低 / 最高 + 等级分布 + 权重版本 + 最新不可调度原因）。
// 只读聚合数字，不涉及任何玩家名单；逐台请求仅覆盖所选少量服务器，非无界循环。

import { useState } from 'react'
import { useQueries } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ArrowUpRight, History, TrendingDown, TrendingUp } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Card,
  CardContent,
  Checkbox,
  IconStat,
  MiniSparkline,
  SectionHeader,
  TableSkeleton,
} from '@beacon/ui'
import type { HealthLevel, HealthSnapshotPoint } from '@beacon/contracts'

import { fetchHealthSnapshots } from '../../api/metrics'
import WindowSelect, { WINDOW_MS, type WindowKey } from './window-select'

// 健康等级 → 状态药丸语义色
function levelBadgeVariant(level: HealthLevel): 'ok' | 'warn' | 'crit' {
  if (level === 'healthy') {
    return 'ok'
  }
  if (level === 'degraded') {
    return 'warn'
  }
  return 'crit'
}

interface SnapshotsPanelProps {
  // 已选 serverId（顺序稳定）
  serverIds: string[]
}

export default function SnapshotsPanel({ serverIds }: SnapshotsPanelProps) {
  const { t } = useTranslation()
  const [windowKey, setWindowKey] = useState<WindowKey>('1h')
  // 冷查询（含归档）开关：开启后跨热 / 冷并表回放（FR-152，无分页、响应形状不变）
  const [cold, setCold] = useState(false)

  // 逐台拉快照回放（受选择数约束）
  const queries = useQueries({
    queries: serverIds.map((serverId) => ({
      queryKey: ['service-analysis', 'health-snapshots', serverId, windowKey, cold],
      queryFn: () => {
        // 时间窗按预设窗口自「现在」往前推（RFC3339，与 metrics/series 取数一致）
        const to = Date.now()
        return fetchHealthSnapshots({
          serverId,
          from: new Date(to - WINDOW_MS[windowKey]).toISOString(),
          to: new Date(to).toISOString(),
          includeArchived: cold ? true : undefined,
        })
      },
    })),
  })

  const isLoading = queries.some((q) => q.isLoading)
  const isError = queries.some((q) => q.isError)
  const error = queries.find((q) => q.error !== null)?.error ?? null

  return (
    <section className="grid gap-3">
      {/* 时间窗控制条吸顶常驻（与指标时序板块同范式） */}
      <div className="sticky top-11 z-10 -mx-1 bg-surface-2/95 px-1 py-1 backdrop-blur supports-backdrop-filter:bg-surface-2/80">
        <SectionHeader
          icon={<History className="size-4" />}
          title={t('observability.serviceAnalysis.snapshots.title')}
          count={t('observability.serviceAnalysis.snapshots.mission')}
          actions={
            <div className="flex items-center gap-3">
              <label
                className="flex cursor-pointer items-center gap-2 text-sm text-ink-2"
                title={t('observability.common.includeArchivedHint')}
              >
                <Checkbox
                  checked={cold}
                  onCheckedChange={(v) => {
                    setCold(v === true)
                  }}
                  aria-label={t('observability.common.includeArchived')}
                />
                {t('observability.common.includeArchived')}
              </label>
              <WindowSelect value={windowKey} keys={['1h', '6h', '24h']} onChange={setWindowKey} />
            </div>
          }
        />
      </div>

      <AsyncSection
        isLoading={isLoading}
        isError={isError}
        error={error}
        skeleton={<TableSkeleton columns={2} rows={serverIds.length || 2} />}
      >
        <div className="grid gap-3 md:grid-cols-2">
          {serverIds.map((serverId, idx) => (
            <SnapshotCard key={serverId} serverId={serverId} points={queries[idx]?.data?.items ?? []} />
          ))}
        </div>
      </AsyncSection>
    </section>
  )
}

// 单服快照回放卡：分数 sparkline + 最新等级 / 权重版本 + 最新 / 最低 / 最高 + 等级分布 + 最新不可调度原因
function SnapshotCard({ serverId, points }: { serverId: string; points: HealthSnapshotPoint[] }) {
  const { t } = useTranslation()
  const latest = points.at(-1)
  const scores = points.map((p) => p.score)
  const levelCount = (level: HealthLevel): number => points.filter((p) => p.level === level).length

  return (
    <Card>
      <CardContent className="grid gap-3">
        <div className="flex items-center gap-2.5">
          <span className="grid size-7 shrink-0 place-items-center rounded-lg bg-brand-50 text-brand">
            <History className="size-[15px]" />
          </span>
          <span className="min-w-0 flex-1 truncate font-mono text-[13px] font-semibold text-ink-1">{serverId}</span>
          {latest !== undefined && (
            <>
              <Badge variant={levelBadgeVariant(latest.level)}>
                {t(`observability.serviceAnalysis.level.${latest.level}`)}
              </Badge>
              <span className="text-[11px] font-medium text-ink-4">
                {t('observability.serviceAnalysis.snapshots.weightsRev', { rev: latest.weightsRev })}
              </span>
            </>
          )}
        </div>

        {latest === undefined ? (
          <p className="rounded-lg border border-dashed border-border bg-card/60 px-3 py-6 text-xs text-ink-3">
            {t('observability.serviceAnalysis.snapshots.empty')}
          </p>
        ) : (
          <>
            <div className="rounded-lg bg-secondary/50 px-2 pt-2">
              <MiniSparkline values={scores} color="var(--brand)" height={56} />
            </div>
            <div className="flex flex-wrap gap-5 border-t border-border pt-3">
              <IconStat
                icon={<ArrowUpRight className="size-4" />}
                label={t('observability.serviceAnalysis.snapshots.latest')}
                value={String(latest.score)}
              />
              <IconStat
                icon={<TrendingDown className="size-4" />}
                label={t('observability.serviceAnalysis.snapshots.min')}
                value={String(Math.min(...scores))}
              />
              <IconStat
                icon={<TrendingUp className="size-4" />}
                label={t('observability.serviceAnalysis.snapshots.max')}
                value={String(Math.max(...scores))}
              />
            </div>
            <div className="flex flex-wrap items-center justify-between gap-2 text-[11px] text-ink-4">
              <span>
                {t('observability.serviceAnalysis.snapshots.levelCounts', {
                  healthy: levelCount('healthy'),
                  degraded: levelCount('degraded'),
                  unhealthy: levelCount('unhealthy'),
                })}
              </span>
              <span>{t('observability.serviceAnalysis.snapshots.points', { count: points.length })}</span>
            </div>
            {latest.reasons.length > 0 && (
              <p className="rounded-lg bg-warn-50/60 px-2.5 py-1.5 text-xs text-warn-800">
                {t('observability.serviceAnalysis.snapshots.latestReasons')}：{latest.reasons.join('、')}
              </p>
            )}
          </>
        )}
      </CardContent>
    </Card>
  )
}
