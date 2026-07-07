// 调度概览卡：决策总数 / 成功率 / 本地降级占比 + 失败原因 Top，下钻到 /service-analysis、/alert-events。
// 数据源 sched-decisions/summary（最近 1h 窗口）。

import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { CircleCheck, Workflow } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  CardGridSkeleton,
  IconStat,
  SectionHeader,
  type HealthLevel,
} from '@beacon/ui'

import { fetchSchedSummary } from '../../api/metrics'

const ICON = 'h-4 w-4'

// 成功率越高越好：>=95% 正常、>=85% 注意、否则危险
function successLevel(percent: number): HealthLevel {
  if (percent >= 95) {
    return 'ok'
  }
  if (percent >= 85) {
    return 'warn'
  }
  return 'danger'
}

export default function SchedOverview() {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['dashboard', 'sched-summary'],
    queryFn: () => fetchSchedSummary('1h'),
  })
  const data = query.data

  return (
    <section className="grid gap-3 rounded-lg border p-4">
      <div className="flex items-center justify-between">
        <SectionHeader title={t('dashboard.sched.title')} />
        <div className="flex gap-3 text-xs">
          <Link className="text-primary hover:underline" to="/service-analysis">
            {t('dashboard.sched.viewAnalysis')}
          </Link>
          <Link className="text-primary hover:underline" to="/alert-events">
            {t('dashboard.sched.viewAlerts')}
          </Link>
        </div>
      </div>
      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<CardGridSkeleton count={3} />}
      >
        {!data || data.total === 0 ? (
          <p className="text-sm text-muted-foreground">{t('dashboard.sched.empty')}</p>
        ) : (
          <div className="grid gap-3">
            <div className="grid grid-cols-3 gap-3">
              <IconStat
                icon={<Workflow className={ICON} />}
                label={t('dashboard.sched.total')}
                value={data.total}
              />
              <IconStat
                icon={<CircleCheck className={ICON} />}
                label={t('dashboard.sched.successRate')}
                value={`${String(data.successRatePercent)}%`}
                level={successLevel(data.successRatePercent)}
              />
              <IconStat
                icon={<Workflow className={ICON} />}
                label={t('dashboard.sched.localFallback')}
                value={`${String(data.localFallbackPercent)}%`}
              />
            </div>
            {data.failReasonTop.length > 0 && (
              <div className="grid gap-1.5">
                <span className="text-xs text-muted-foreground">{t('dashboard.sched.failTop')}</span>
                <ul className="flex flex-wrap gap-1.5">
                  {data.failReasonTop.slice(0, 3).map((reason) => (
                    <li key={reason.reason}>
                      <Badge variant="outline">
                        {reason.reason} · {reason.count}
                      </Badge>
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        )}
      </AsyncSection>
    </section>
  )
}
