// 告警概览卡（对齐 B 版）：图标标题 + 未处理数 + 危急/警告/提示计数药丸 + 最新告警列表
// （左侧等级图标框 + 服务器名 + 摘要）。下钻到 /alert-events。数据源 alert-events 列表。

import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { CircleAlert, Info, TriangleAlert } from 'lucide-react'

import { AsyncSection, Badge, CardGridSkeleton, SectionHeader, cn } from '@beacon/ui'
import type { AlertEventItem } from '@beacon/contracts'

import { fetchAlertEvents } from '../../api/observability'
import { fetchPagedItemsByEnvScope, useEnvNamespaceCodes, useEnvScopePending } from '../../features/env/use-env-scope'
import { alertSubtitle } from '../../features/observability/alert-transition'

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
  // FR-178：告警概览按每个 env 的命名空间受限请求。
  const envCodes = useEnvNamespaceCodes()
  // 观测范围仍在解析（env 选项未就绪）时显示骨架，不把「范围待解析」误报成空态。
  const envPending = useEnvScopePending()
  const query = useQuery({
    queryKey: ['dashboard', 'alerts', envCodes],
    queryFn: () =>
      fetchPagedItemsByEnvScope(
        envCodes,
        (namespace, pageRequest) => fetchAlertEvents({ page: 1, size: pageRequest?.pageSize ?? 100, namespace }),
        { page: 1, pageSize: 100, compare: (left, right) => Date.parse(right.createdAt) - Date.parse(left.createdAt) },
      ),
    // 观测范围未就绪时不发请求，交回 react-query 原生 pending 态（骨架由此承接）
    enabled: !envPending,
  })
  const items = useMemo(() => query.data?.items ?? [], [query.data])

  const openItems = items.filter((i) => i.status === 'open')
  const criticalOpen = openItems.filter((i) => i.level === 'critical').length
  const warningOpen = openItems.filter((i) => i.level === 'warning').length
  const infoOpen = openItems.filter((i) => i.level !== 'critical' && i.level !== 'warning').length
  // 只列 4 条（配下方限高自区滚），一屏放得下更多区段
  const latest: AlertEventItem[] = openItems.slice(0, 4)

  // 等级计数药丸（仅在计数 > 0 时出现）
  const sevBadges = (
    <>
      {criticalOpen > 0 && <Badge variant="crit">{t('dashboard.alerts.critical')} {criticalOpen}</Badge>}
      {warningOpen > 0 && <Badge variant="warn">{t('dashboard.alerts.warning')} {warningOpen}</Badge>}
      {infoOpen > 0 && <Badge variant="brand">{t('dashboard.alerts.info')} {infoOpen}</Badge>}
    </>
  )

  // 下钻链接：href 与文案不变
  const viewAllLink = (
    <Link className="text-xs text-brand-600 hover:underline" to="/alert-events">
      {t('dashboard.alerts.viewAll')}
    </Link>
  )

  // 标题行：复用 @beacon/ui SectionHeader；「查看告警事件」在此，列表内不再重复
  const header = (
    <SectionHeader
      icon={<TriangleAlert className="size-4" />}
      title={t('dashboard.alerts.title')}
      count={`${t('dashboard.alerts.open')} ${String(openItems.length)}`}
      actions={
        <>
          {sevBadges}
          {viewAllLink}
        </>
      }
    />
  )

  return (
    <section className="grid grid-cols-1 grid-rows-[auto_1fr] gap-3 rounded-xl border border-border bg-card p-3.5 shadow-card">
      {header}
      <AsyncSection
        isLoading={query.isPending}
        isError={query.isError}
        error={query.error}
        skeleton={<CardGridSkeleton count={2} />}
      >
        {openItems.length === 0 ? (
          <p className="text-sm text-ink-3">{t('dashboard.alerts.empty')}</p>
        ) : (
          <div className="grid min-w-0 gap-2">
            <ul className="flex max-h-[9.5rem] min-w-0 flex-col overflow-y-auto">
              {latest.map((item) => {
                const meta = sevMeta(item.level)
                const Icon = meta.icon
                return (
                  <li
                    key={item.id}
                    className="flex min-w-0 items-center gap-3 border-b border-border py-2 last:border-b-0"
                  >
                    <span className={cn('grid size-[26px] shrink-0 place-items-center rounded-lg', meta.box)}>
                      <Icon className="size-[15px]" />
                    </span>
                    <div className="min-w-0 flex-1">
                      <div className="truncate text-[12.5px] text-ink-2">
                        <span className="font-semibold text-brand-600">{item.serverId}</span> ·{' '}
                        {alertSubtitle(item, t)}
                      </div>
                    </div>
                  </li>
                )
              })}
            </ul>
          </div>
        )}
      </AsyncSection>
    </section>
  )
}
