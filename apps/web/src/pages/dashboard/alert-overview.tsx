// 告警概览卡：待处理 / 严重告警计数 + 最新几条告警摘要，下钻到 /alert-events。
// 数据源 alert-events 列表（取首页派生计数与最新条目）。

import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { BellRing, TriangleAlert } from 'lucide-react'

import {
  AsyncSection,
  CardGridSkeleton,
  IconStat,
  SectionHeader,
  countLevel,
} from '@beacon/ui'

import { fetchAlertEvents } from '../../api/observability'

const ICON = 'h-4 w-4'

export default function AlertOverview() {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['dashboard', 'alerts'],
    queryFn: () => fetchAlertEvents({ page: 1, size: 100 }),
  })
  const items = useMemo(() => query.data?.items ?? [], [query.data])

  const openItems = items.filter((i) => i.status === 'open')
  const criticalOpen = openItems.filter((i) => i.level === 'critical').length
  const latest = openItems.slice(0, 3)

  return (
    <section className="grid gap-3 rounded-lg border p-4">
      <div className="flex items-center justify-between">
        <SectionHeader title={t('dashboard.alerts.title')} />
        <Link className="text-xs text-primary hover:underline" to="/alert-events">
          {t('dashboard.alerts.viewAll')}
        </Link>
      </div>
      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<CardGridSkeleton count={2} />}
      >
        {openItems.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('dashboard.alerts.empty')}</p>
        ) : (
          <div className="grid gap-3">
            <div className="grid grid-cols-2 gap-3">
              <IconStat
                icon={<BellRing className={ICON} />}
                label={t('dashboard.alerts.open')}
                value={openItems.length}
                level={countLevel(openItems.length)}
              />
              <IconStat
                icon={<TriangleAlert className={ICON} />}
                label={t('dashboard.alerts.critical')}
                value={criticalOpen}
                level={countLevel(criticalOpen)}
              />
            </div>
            <div className="grid gap-1.5">
              <span className="text-xs text-muted-foreground">{t('dashboard.alerts.latest')}</span>
              <ul className="grid gap-1">
                {latest.map((item) => (
                  <li key={item.id} className="truncate text-xs">
                    <span className="font-mono text-muted-foreground">{item.serverId}</span> · {item.message}
                  </li>
                ))}
              </ul>
            </div>
          </div>
        )}
      </AsyncSection>
    </section>
  )
}
