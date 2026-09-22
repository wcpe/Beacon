// 服务器状态墙（对齐 B 版）：从 /admin/v2/health 列表取逐服健康——类型图标 + 归属标签 +
// 状态药丸（在线/降级/危急/离线）+ 行内健康分 mini-bar。只看不改，下钻到 /servers。
// 数据源 metrics/health（真实字段：serverId / kind / zoneName / score / level）。

import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { ChevronRight, Network, Server } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
  TableSkeleton,
  cn,
  levelSolid,
  type HealthLevel,
} from '@beacon/ui'
import type { HealthItem } from '@beacon/contracts'

import { fetchHealthList } from '../../api/metrics'
import { fetchPagedItemsByEnvScope, useEnvNamespaceScope, useEnvScopePending } from '../../features/env/use-env-scope'

// 展示上限：状态墙只列前若干台，全量在 /servers。
// 多命名空间分别受限请求后合并，保证不依赖客户端后过滤。
const WALL_LIMIT = 12

// 健康等级（mock 的 healthy/degraded/unhealthy）→ 设计语言等级 + 药丸变体 + 文案键。
const LEVEL_META: Record<
  HealthItem['level'],
  { level: HealthLevel; variant: 'ok' | 'warn' | 'crit'; labelKey: string }
> = {
  healthy: { level: 'ok', variant: 'ok', labelKey: 'dashboard.wall.online' },
  degraded: { level: 'warn', variant: 'warn', labelKey: 'dashboard.wall.degraded' },
  unhealthy: { level: 'danger', variant: 'crit', labelKey: 'dashboard.wall.critical' },
}

export default function ServerWall() {
  const { t } = useTranslation()
  // FR-178：状态墙跟随顶栏 env
  const envScope = useEnvNamespaceScope()
  // 观测范围仍在解析（env 选项未就绪）时显示骨架，不把「范围待解析」误报成空态。
  const envPending = useEnvScopePending()
  const query = useQuery({
    queryKey: ['dashboard', 'health-list', envScope],
    queryFn: () =>
      fetchPagedItemsByEnvScope(
        envScope,
        (namespaceId, pageRequest) => fetchHealthList({ namespaceId, pageSize: pageRequest?.pageSize ?? WALL_LIMIT }),
        {
          page: 1,
          pageSize: WALL_LIMIT,
          compare: (left, right) =>
            left.namespaceId - right.namespaceId || left.serverId.localeCompare(right.serverId),
        },
      ),
    // 观测范围未就绪时不发请求，交回 react-query 原生 pending 态（骨架由此承接）
    enabled: !envPending,
  })
  const items = useMemo(() => (query.data?.items ?? []).slice(0, WALL_LIMIT), [query.data])

  return (
    <section className="grid gap-3 rounded-xl border border-border bg-card p-4 shadow-card">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2.5">
          <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
            <Server className="size-[15px]" />
          </span>
          <h2 className="text-[13px] font-semibold text-ink-1">{t('dashboard.wall.title')}</h2>
        </div>
        <Link
          className="flex items-center gap-0.5 rounded-md px-1.5 py-1 text-[11.5px] text-ink-4 hover:bg-surface-2 hover:text-brand"
          to="/servers"
        >
          {t('dashboard.wall.viewAll')}
          <ChevronRight className="size-3" />
        </Link>
      </div>
      <AsyncSection
        isLoading={query.isPending}
        isError={query.isError}
        error={query.error}
        skeleton={<TableSkeleton columns={4} rows={6} />}
      >
        {items.length === 0 ? (
          <p className="text-sm text-ink-3">{t('dashboard.wall.empty')}</p>
        ) : (
          <Table className="tnum">
            <TableHeader>
              <TableRow>
                <TableHead>{t('dashboard.wall.server')}</TableHead>
                <TableHead>{t('dashboard.wall.zone')}</TableHead>
                <TableHead>{t('dashboard.wall.status')}</TableHead>
                <TableHead>{t('dashboard.wall.score')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((item) => {
                const meta = LEVEL_META[item.level]
                const isProxy = item.kind === 'proxy'
                return (
                  <TableRow key={item.serverId}>
                    <TableCell>
                      <div className="flex items-center gap-2 font-semibold text-ink-1">
                        <span
                          className={cn(
                            'grid size-5 place-items-center rounded-md',
                            isProxy ? 'bg-brand-100 text-brand-600' : 'bg-brand-50 text-brand',
                          )}
                          aria-hidden
                        >
                          {isProxy ? (
                            <Network className="size-3" />
                          ) : (
                            <Server className="size-3" />
                          )}
                        </span>
                        {item.serverId}
                      </div>
                    </TableCell>
                    <TableCell>
                      {item.zoneName ? (
                        <span className="rounded-md border border-border-strong bg-surface-2 px-1.5 py-0.5 text-[11px] text-ink-3">
                          {item.zoneName}
                        </span>
                      ) : (
                        <Badge variant="off">{t('dashboard.wall.unassigned')}</Badge>
                      )}
                    </TableCell>
                    <TableCell>
                      <Badge variant={meta.variant} className="gap-1.5">
                        <span className="size-1.5 rounded-full bg-current" />
                        {t(meta.labelKey)}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <div className="flex items-center gap-2">
                        <span className="h-1.5 w-14 overflow-hidden rounded-full bg-muted">
                          <span
                            className={cn('block h-full rounded-full', levelSolid(meta.level))}
                            style={{ width: `${String(Math.max(0, Math.min(100, item.score)))}%` }}
                          />
                        </span>
                        <span className="w-9 text-right text-ink-2">{item.score}</span>
                      </div>
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        )}
      </AsyncSection>
    </section>
  )
}
