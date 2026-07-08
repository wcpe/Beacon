// 顶部 KPI 卡行（对齐 B 版 row-kpi）：可调度服务器 / 代理·子服在线 / 在线玩家 / 平均 TPS
// + 健康等级分布环。数据源 metrics/summary（与健康总览同 queryKey，React Query 去重复用）。

import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Activity, Gauge, Network, Server, Users } from 'lucide-react'

import {
  AsyncSection,
  CardGridSkeleton,
  KpiCard,
  cn,
  levelSolid,
  type HealthLevel,
} from '@beacon/ui'

import { fetchMetricsSummary } from '../../api/metrics'

// 细进度条（KPI 底部可视化槽）：按等级上色。
function MiniBar({ pct, level }: { pct: number; level: HealthLevel }) {
  return (
    <div className="h-1.5 overflow-hidden rounded-full bg-muted">
      <div
        className={cn('h-full rounded-full', levelSolid(level))}
        style={{ width: `${String(Math.max(0, Math.min(100, pct)))}%` }}
      />
    </div>
  )
}

// 健康等级分布环（SVG stroke-dasharray）：健康 / 亚健康 / 不健康 三段占比，中心显总数。
function DistributionRing({
  healthy,
  degraded,
  unhealthy,
}: {
  healthy: number
  degraded: number
  unhealthy: number
}) {
  const total = healthy + degraded + unhealthy
  // 各段占周长百分比（总周长按 100 计），依次首尾相接。
  const segs =
    total === 0
      ? []
      : [
          { color: 'var(--ok)', pct: (healthy / total) * 100 },
          { color: 'var(--warn)', pct: (degraded / total) * 100 },
          { color: 'var(--crit)', pct: (unhealthy / total) * 100 },
        ]
  let offset = 25 // 从 12 点方向起画（dashoffset 25 = 顶部）
  return (
    <svg width="86" height="86" viewBox="0 0 42 42" className="shrink-0" role="img" aria-label="健康等级分布">
      <circle cx="21" cy="21" r="15.9" fill="none" stroke="var(--muted)" strokeWidth="6" />
      {segs.map((s, i) => {
        const dash = `${String(s.pct)} ${String(100 - s.pct)}`
        const el = (
          <circle
            key={i}
            cx="21"
            cy="21"
            r="15.9"
            fill="none"
            stroke={s.color}
            strokeWidth="6"
            strokeDasharray={dash}
            strokeDashoffset={String(offset)}
            strokeLinecap="round"
          />
        )
        offset -= s.pct
        return el
      })}
      <text x="21" y="20.5" textAnchor="middle" fontSize="8" fontWeight="700" fill="var(--ink-1)">
        {total}
      </text>
      <text x="21" y="27" textAnchor="middle" fontSize="3.4" fill="var(--ink-4)">
        总实例
      </text>
    </svg>
  )
}

export default function KpiStrip() {
  const { t } = useTranslation()
  const query = useQuery({ queryKey: ['dashboard', 'metrics-summary'], queryFn: fetchMetricsSummary })
  const data = query.data

  return (
    <AsyncSection
      isLoading={query.isLoading}
      isError={query.isError}
      error={query.error}
      skeleton={<CardGridSkeleton count={5} />}
    >
      {data && (
        <div className="grid gap-3.5 sm:grid-cols-2 xl:grid-cols-[repeat(4,1fr)_1.25fr]">
          {/* 可调度服务器 */}
          <KpiCard
            label={t('dashboard.kpi.schedulable')}
            value={data.schedulable.yes}
            unit={` / ${String(data.schedulable.yes + data.schedulable.no)} ${t('dashboard.kpi.units')}`}
            icon={<Server className="size-4" />}
            tone="brand"
            visual={
              <MiniBar
                pct={
                  data.schedulable.yes + data.schedulable.no === 0
                    ? 0
                    : (data.schedulable.yes / (data.schedulable.yes + data.schedulable.no)) * 100
                }
                level="ok"
              />
            }
            meta={t('dashboard.kpi.schedulableHint')}
          />

          {/* 代理 / 子服在线 */}
          <KpiCard
            label={t('dashboard.kpi.online')}
            value={
              <>
                {data.byKind.proxy.online}
                <span className="text-[14px] font-semibold text-ink-4">
                  /{data.byKind.proxy.total}
                </span>
                <span className="px-1.5 font-normal text-ink-4">·</span>
                {data.byKind.backend.online}
                <span className="text-[14px] font-semibold text-ink-4">
                  /{data.byKind.backend.total}
                </span>
              </>
            }
            icon={<Network className="size-4" />}
            tone="brand"
            meta={
              <>
                {t('dashboard.kpi.proxy')} {data.byKind.proxy.online}/{data.byKind.proxy.total} ·{' '}
                {t('dashboard.kpi.backend')} {data.byKind.backend.online}/{data.byKind.backend.total}
              </>
            }
          />

          {/* 在线玩家 */}
          <KpiCard
            label={t('dashboard.kpi.players')}
            value={data.playersOnline}
            icon={<Users className="size-4" />}
            tone="ok"
            meta={t('dashboard.kpi.playersHint')}
          />

          {/* 平均 TPS */}
          <KpiCard
            label={t('dashboard.kpi.avgTps')}
            value={data.avgTps.toFixed(1)}
            icon={<Gauge className="size-4" />}
            tone={data.avgTps >= 19.5 ? 'ok' : data.avgTps >= 18 ? 'warn' : 'crit'}
            visual={<MiniBar pct={(data.avgTps / 20) * 100} level={data.avgTps >= 18 ? 'ok' : 'warn'} />}
            meta={
              <>
                {t('dashboard.kpi.target')} 20.0 · {t('dashboard.kpi.avgCpu')}{' '}
                {data.avgCpuPct.toFixed(1)}%
              </>
            }
          />

          {/* 健康等级分布环 */}
          <div className="flex flex-col gap-2 rounded-xl border border-border bg-card p-[15px] shadow-card">
            <div className="flex items-center gap-2">
              <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
                <Activity className="size-[15px]" />
              </span>
              <span className="text-[13px] font-semibold text-ink-1">
                {t('dashboard.kpi.distribution')}
              </span>
            </div>
            <div className="flex items-center gap-4">
              <DistributionRing
                healthy={data.levelDistribution.healthy}
                degraded={data.levelDistribution.degraded}
                unhealthy={data.levelDistribution.unhealthy}
              />
              <div className="flex flex-1 flex-col gap-2 text-[12px]">
                <LegendRow
                  color="var(--ok)"
                  name={t('dashboard.kpi.levelHealthy')}
                  value={data.levelDistribution.healthy}
                />
                <LegendRow
                  color="var(--warn)"
                  name={t('dashboard.kpi.levelDegraded')}
                  value={data.levelDistribution.degraded}
                />
                <LegendRow
                  color="var(--crit)"
                  name={t('dashboard.kpi.levelUnhealthy')}
                  value={data.levelDistribution.unhealthy}
                />
              </div>
            </div>
          </div>
        </div>
      )}
    </AsyncSection>
  )
}

// 环形图例单行：色块 + 名称 + 计数。
function LegendRow({ color, name, value }: { color: string; name: string; value: number }) {
  return (
    <div className="flex items-center gap-2">
      <span className="size-2.5 rounded-[3px]" style={{ background: color }} aria-hidden />
      <span className="text-ink-2">{name}</span>
      <span className="ml-auto font-bold text-ink-1 tnum">{value}</span>
    </div>
  )
}
