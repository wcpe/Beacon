// 玩家流 / 连接流卡：最近 1 小时连接时间桶——在线连接 / 新建连接趋势 sparkline + 峰值 / 累计 / 异常。
// 只画聚合数字，不涉及玩家名单。数据源 connections/stats。

import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Activity, TrendingUp, TriangleAlert } from 'lucide-react'

import {
  AsyncSection,
  CardGridSkeleton,
  IconStat,
  MiniSparkline,
  SectionHeader,
  countLevel,
} from '@beacon/ui'
import { BASE_MS } from '@beacon/devmock'

import { fetchConnStats } from '../../api/connections'

const ICON = 'h-4 w-4'
const WINDOW_MS = 3_600_000

export default function FlowOverview() {
  const { t } = useTranslation()
  // 显式给最近 1h 窗口（相对 mock 时间基准，命中前 45 分钟连接数据）
  const to = useMemo(() => new Date(BASE_MS).toISOString(), [])
  const from = useMemo(() => new Date(BASE_MS - WINDOW_MS).toISOString(), [])

  const query = useQuery({
    queryKey: ['dashboard', 'conn-stats', from, to],
    queryFn: () => fetchConnStats({ from, to, bucket: '5m' }),
  })
  const buckets = useMemo(() => query.data?.buckets ?? [], [query.data])

  const opensSeries = buckets.map((b) => b.opens)
  const onlineSeries = buckets.map((b) => b.estimatedOpen)
  const peakOnline = onlineSeries.length === 0 ? 0 : Math.max(...onlineSeries)
  const totalOpens = opensSeries.reduce((sum, v) => sum + v, 0)
  const totalAbnormal = buckets.reduce((sum, b) => sum + b.abnormalCloses, 0)

  const isEmpty = buckets.length === 0 || (totalOpens === 0 && peakOnline === 0)

  return (
    <section className="grid gap-3 rounded-lg border p-4">
      <SectionHeader title={t('dashboard.flow.title')} />
      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<CardGridSkeleton count={3} />}
      >
        {isEmpty ? (
          <p className="text-sm text-muted-foreground">{t('dashboard.flow.empty')}</p>
        ) : (
          <div className="grid gap-3">
            <div className="grid grid-cols-3 gap-3">
              <IconStat
                icon={<Activity className={ICON} />}
                label={t('dashboard.flow.peakOnline')}
                value={peakOnline}
              />
              <IconStat
                icon={<TrendingUp className={ICON} />}
                label={t('dashboard.flow.totalOpens')}
                value={totalOpens}
              />
              <IconStat
                icon={<TriangleAlert className={ICON} />}
                label={t('dashboard.flow.totalAbnormal')}
                value={totalAbnormal}
                level={countLevel(totalAbnormal)}
              />
            </div>
            <div className="grid gap-1">
              <span className="text-xs text-muted-foreground">{t('dashboard.flow.estimatedOpen')}</span>
              <MiniSparkline values={onlineSeries} color="var(--primary)" height={40} />
            </div>
            <div className="grid gap-1">
              <span className="text-xs text-muted-foreground">{t('dashboard.flow.opens')}</span>
              <MiniSparkline values={opensSeries} color="var(--primary)" height={32} />
            </div>
          </div>
        )}
      </AsyncSection>
    </section>
  )
}
