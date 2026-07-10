// 告警概览卡（对齐 B 版）：图标标题 + 未处理数 + 危急/警告/提示计数药丸 + 最新告警列表
// （左侧等级图标框 + 服务器名 + 摘要）。下钻到 /alert-events。数据源 alert-events 列表。

import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { CircleAlert, Info, TriangleAlert } from 'lucide-react'

import { AsyncSection, Badge, CardGridSkeleton, cn } from '@beacon/ui'
import type { AlertEventItem } from '@beacon/contracts'

import { fetchAlertEvents } from '../../api/observability'

// 告警等级 → 图标框样式 + 图标。
const SEV_META: Record<
  string,
  { box: string; icon: typeof CircleAlert }
> = {
  critical: { box: 'bg-crit-bg text-crit', icon: CircleAlert },
  warning: { box: 'bg-warn-bg text-warn', icon: TriangleAlert },
  info: { box: 'bg-brand-50 text-brand', icon: Info },
}

function sevMeta(level: string) {
  return SEV_META[level] ?? SEV_META.info
}

export default function AlertOverview() {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['dashboard', 'alerts'],
    queryFn: () => fetchAlertEvents({ page: 1, size: 100 }),
  })
  const items = useMemo(() => query.data?.items ?? [], [query.data])

  const openItems = items.filter((i) => i.status === 'open')
  const criticalOpen = openItems.filter((i) => i.level === 'critical').length
  const warningOpen = openItems.filter((i) => i.level === 'warning').length
  const infoOpen = openItems.filter((i) => i.level !== 'critical' && i.level !== 'warning').length
  const latest: AlertEventItem[] = openItems.slice(0, 5)

  return (
    <section className="grid grid-rows-[auto_1fr] gap-3 rounded-xl border border-border bg-card p-4 shadow-card">
      <div className="flex items-center gap-2.5">
        <span className="grid size-[26px] place-items-center rounded-lg bg-crit-bg text-crit">
          <TriangleAlert className="size-[15px]" />
        </span>
        <h2 className="text-[13px] font-semibold text-ink-1">{t('dashboard.alerts.title')}</h2>
        <span className="text-[11px] text-ink-4">
          {t('dashboard.alerts.open')} {openItems.length}
        </span>
        <div className="ml-auto flex gap-2">
          {criticalOpen > 0 && <Badge variant="crit">{t('dashboard.alerts.critical')} {criticalOpen}</Badge>}
          {warningOpen > 0 && <Badge variant="warn">{t('dashboard.alerts.warning')} {warningOpen}</Badge>}
          {infoOpen > 0 && <Badge variant="brand">{t('dashboard.alerts.info')} {infoOpen}</Badge>}
        </div>
      </div>
      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<CardGridSkeleton count={2} />}
      >
        {openItems.length === 0 ? (
          <div className="flex items-center justify-between">
            <p className="text-sm text-ink-3">{t('dashboard.alerts.empty')}</p>
            <Link className="text-xs text-brand-600 hover:underline" to="/alert-events">
              {t('dashboard.alerts.viewAll')}
            </Link>
          </div>
        ) : (
          <div className="grid gap-2">
            <ul className="flex flex-col">
              {latest.map((item) => {
                const meta = sevMeta(item.level)
                const Icon = meta.icon
                return (
                  <li
                    key={item.id}
                    className="flex items-center gap-3 border-b border-border py-2.5 last:border-b-0"
                  >
                    <span className={cn('grid size-[30px] shrink-0 place-items-center rounded-lg', meta.box)}>
                      <Icon className="size-[15px]" />
                    </span>
                    <div className="min-w-0 flex-1">
                      <div className="truncate text-[12.5px] text-ink-2">
                        <span className="font-semibold text-brand-600">{item.serverId}</span> ·{' '}
                        {item.message}
                      </div>
                    </div>
                  </li>
                )
              })}
            </ul>
            <Link
              className="text-xs text-brand-600 hover:underline"
              to="/alert-events"
            >
              {t('dashboard.alerts.viewAll')}
            </Link>
          </div>
        )}
      </AsyncSection>
    </section>
  )
}
