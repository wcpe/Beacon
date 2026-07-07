// 集群健康总览卡：分角色在线（代理 / 子服）+ 健康等级分布条 + 可调度占比环 + 关键均值。
// 异常可下钻到 /servers。数据源 metrics/summary。

import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { Activity, Cpu, HeartPulse, Server, Users } from 'lucide-react'

import {
  AsyncSection,
  CardGridSkeleton,
  GaugeRing,
  HealthBar,
  IconStat,
  SectionHeader,
  ratioLevel,
  type HealthSegment,
} from '@beacon/ui'

import { fetchMetricsSummary } from '../../api/metrics'

const ICON = 'h-4 w-4'

export default function HealthOverview() {
  const { t } = useTranslation()
  const query = useQuery({ queryKey: ['dashboard', 'metrics-summary'], queryFn: fetchMetricsSummary })
  const data = query.data

  // 无任何服务器视为空态（接入引导）
  const isEmpty = data?.byKind.proxy.total === 0 && data.byKind.backend.total === 0

  const segments: HealthSegment[] = data
    ? [
        { label: t('dashboard.health.levelHealthy'), count: data.levelDistribution.healthy, level: 'ok' },
        { label: t('dashboard.health.levelDegraded'), count: data.levelDistribution.degraded, level: 'warn' },
        { label: t('dashboard.health.levelUnhealthy'), count: data.levelDistribution.unhealthy, level: 'danger' },
      ]
    : []

  const schedTotal = data ? data.schedulable.yes + data.schedulable.no : 0
  const schedRatio = schedTotal === 0 ? null : data ? data.schedulable.yes / schedTotal : null

  return (
    <section className="grid gap-3 rounded-lg border p-4">
      <div className="flex items-center justify-between">
        <SectionHeader title={t('dashboard.health.title')} />
        <Link className="text-xs text-primary hover:underline" to="/servers">
          {t('dashboard.health.viewServers')}
        </Link>
      </div>

      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<CardGridSkeleton count={4} />}
      >
        {isEmpty ? (
          <p className="text-sm text-muted-foreground">{t('dashboard.health.empty')}</p>
        ) : (
          data && (
            <div className="grid gap-4 sm:grid-cols-[auto_1fr]">
              <GaugeRing
                icon={<HeartPulse className="h-5 w-5" />}
                ratio={schedRatio}
                level={schedRatio === null ? 'muted' : ratioLevel(schedRatio, 0, 0)}
                label={t('dashboard.health.schedulable')}
                valueText={`${String(data.schedulable.yes)} / ${String(schedTotal)}`}
              />
              <div className="grid gap-3">
                <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
                  <IconStat
                    icon={<Server className={ICON} />}
                    label={t('dashboard.health.proxy')}
                    value={`${String(data.byKind.proxy.online)} / ${String(data.byKind.proxy.total)}`}
                  />
                  <IconStat
                    icon={<Server className={ICON} />}
                    label={t('dashboard.health.backend')}
                    value={`${String(data.byKind.backend.online)} / ${String(data.byKind.backend.total)}`}
                  />
                  <IconStat
                    icon={<Users className={ICON} />}
                    label={t('dashboard.health.players')}
                    value={data.playersOnline}
                  />
                  <IconStat
                    icon={<Activity className={ICON} />}
                    label={t('dashboard.health.avgTps')}
                    value={data.avgTps.toFixed(1)}
                  />
                </div>
                <div className="grid gap-1.5">
                  <div className="flex items-center justify-between text-xs text-muted-foreground">
                    <span>{t('dashboard.health.distribution')}</span>
                    <span className="flex items-center gap-1">
                      <Cpu className="h-3 w-3" />
                      {t('dashboard.health.avgCpu')} {data.avgCpuPct.toFixed(1)}%
                    </span>
                  </div>
                  <HealthBar segments={segments} />
                  <div className="flex flex-wrap gap-3 text-xs text-muted-foreground">
                    <span>
                      {t('dashboard.health.levelHealthy')} {data.levelDistribution.healthy}
                    </span>
                    <span>
                      {t('dashboard.health.levelDegraded')} {data.levelDistribution.degraded}
                    </span>
                    <span>
                      {t('dashboard.health.levelUnhealthy')} {data.levelDistribution.unhealthy}
                    </span>
                  </div>
                </div>
              </div>
            </div>
          )
        )}
      </AsyncSection>
    </section>
  )
}
