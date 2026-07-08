// 玩家流 / 连接流卡（对齐 B 版）：最近 1 小时连接时间桶——在线连接 / 新建连接自绘 SVG 面积 + 折线，
// 底部当前玩家 / 当前连接 / 小时峰值。只画聚合数字，不涉及玩家名单。数据源 connections/stats。

import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ChartLine, ChevronRight } from 'lucide-react'

import { AsyncSection, CardGridSkeleton } from '@beacon/ui'
import { BASE_MS } from '@beacon/devmock'

import { fetchConnStats } from '../../api/connections'

const WINDOW_MS = 3_600_000
// SVG 画布尺寸（viewBox），随卡片宽度自适应。
const VW = 560
const VH = 200

// 把数值序列映射为 SVG 折线点串（x 均分、y 按序列域归一到 [pad, VH-pad]）。
function toPoints(values: number[], max: number): { x: number; y: number }[] {
  if (values.length === 0) {
    return []
  }
  const pad = 12
  const span = VH - pad * 2
  const denom = max <= 0 ? 1 : max
  const step = values.length === 1 ? 0 : VW / (values.length - 1)
  return values.map((v, i) => ({
    x: i * step,
    y: VH - pad - (Math.max(0, v) / denom) * span,
  }))
}

function polyline(points: { x: number; y: number }[]): string {
  return points.map((p) => `${String(p.x)},${String(p.y)}`).join(' ')
}

export default function FlowOverview() {
  const { t } = useTranslation()
  const to = useMemo(() => new Date(BASE_MS).toISOString(), [])
  const from = useMemo(() => new Date(BASE_MS - WINDOW_MS).toISOString(), [])

  const query = useQuery({
    queryKey: ['dashboard', 'conn-stats', from, to],
    queryFn: () => fetchConnStats({ from, to, bucket: '5m' }),
  })
  const buckets = useMemo(() => query.data?.buckets ?? [], [query.data])

  const onlineSeries = buckets.map((b) => b.estimatedOpen)
  const opensSeries = buckets.map((b) => b.opens)
  const peakOnline = onlineSeries.length === 0 ? 0 : Math.max(...onlineSeries)
  const totalOpens = opensSeries.reduce((sum, v) => sum + v, 0)
  const currentOnline = onlineSeries.length === 0 ? 0 : onlineSeries[onlineSeries.length - 1]
  const currentOpens = opensSeries.length === 0 ? 0 : opensSeries[opensSeries.length - 1]

  const isEmpty = buckets.length === 0 || (totalOpens === 0 && peakOnline === 0)

  // 两条折线共用同一 y 域（取两序列最大值），保证可比。
  const yMax = Math.max(peakOnline, totalOpens === 0 ? 0 : Math.max(...opensSeries), 1)
  const onlinePts = toPoints(onlineSeries, yMax)
  const opensPts = toPoints(opensSeries, yMax)
  const areaPath =
    onlinePts.length === 0
      ? ''
      : `M${polyline(onlinePts).replace(/ /g, ' L')} L${String(VW)},${String(VH)} L0,${String(VH)} Z`

  return (
    <section className="grid grid-rows-[auto_1fr] gap-3 rounded-xl border border-border bg-card p-4 shadow-card">
      <div className="flex items-center gap-2.5">
        <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
          <ChartLine className="size-[15px]" />
        </span>
        <h2 className="text-[13px] font-semibold text-ink-1">{t('dashboard.flow.title')}</h2>
        <span className="text-[11px] text-ink-4">{t('dashboard.flow.window1h')}</span>
        <span className="ml-auto flex items-center gap-0.5 text-[11.5px] text-ink-4">
          {t('dashboard.flow.detail')}
          <ChevronRight className="size-3" />
        </span>
      </div>
      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<CardGridSkeleton count={3} />}
      >
        {isEmpty ? (
          <p className="text-sm text-ink-3">{t('dashboard.flow.empty')}</p>
        ) : (
          <div className="grid gap-3">
            {/* 图例 */}
            <div className="flex gap-4">
              <span className="flex items-center gap-1.5 text-[11.5px] text-ink-3">
                <span className="h-[3px] w-3.5 rounded-full bg-brand" />
                {t('dashboard.flow.estimatedOpen')}
              </span>
              <span className="flex items-center gap-1.5 text-[11.5px] text-ink-3">
                <span className="h-[3px] w-3.5 rounded-full bg-off" />
                {t('dashboard.flow.opens')}
              </span>
            </div>
            {/* 自绘 SVG 面积 + 双折线 */}
            <svg
              viewBox={`0 0 ${String(VW)} ${String(VH)}`}
              preserveAspectRatio="none"
              className="h-40 w-full"
              role="img"
              aria-label={t('dashboard.flow.title')}
            >
              <defs>
                <linearGradient id="flowPlayerFill" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor="var(--brand)" stopOpacity="0.22" />
                  <stop offset="100%" stopColor="var(--brand)" stopOpacity="0" />
                </linearGradient>
              </defs>
              {/* 网格线 */}
              <g stroke="var(--border)" strokeWidth="1">
                {[0.25, 0.5, 0.75].map((r) => (
                  <line key={r} x1="0" y1={VH * r} x2={VW} y2={VH * r} />
                ))}
              </g>
              {areaPath !== '' && <path d={areaPath} fill="url(#flowPlayerFill)" />}
              <polyline
                fill="none"
                stroke="var(--brand)"
                strokeWidth="2.5"
                strokeLinecap="round"
                strokeLinejoin="round"
                points={polyline(onlinePts)}
              />
              <polyline
                fill="none"
                stroke="var(--off)"
                strokeWidth="2"
                strokeDasharray="4 4"
                strokeLinecap="round"
                strokeLinejoin="round"
                points={polyline(opensPts)}
              />
            </svg>
            {/* 底部摘要 */}
            <div className="flex gap-3.5 border-t border-border pt-3">
              <SummaryCell label={t('dashboard.flow.currentPlayers')} value={currentOnline} />
              <SummaryCell label={t('dashboard.flow.currentOpens')} value={currentOpens} />
              <SummaryCell label={t('dashboard.flow.peakOnline')} value={peakOnline} />
            </div>
          </div>
        )}
      </AsyncSection>
    </section>
  )
}

// 底部摘要单元：弱色标签 + 大数值。
function SummaryCell({ label, value }: { label: string; value: number }) {
  return (
    <div className="flex-1">
      <div className="text-[11px] text-ink-4">{label}</div>
      <div className="text-lg font-bold text-ink-1 tnum">{value}</div>
    </div>
  )
}
