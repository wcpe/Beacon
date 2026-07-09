// 服务器资产主列表（页面主体）：紧凑 KPI 一行 + 吸顶筛选/操作条（keyword + 类型 + 分配状态 +
// 待确认入口 + 批量操作）+ 高密度分页表。列表区自身滚动，筛选条 sticky 吸顶不被推走。
// 点某行看健康详情走右侧抽屉（onViewHealth 回调），绝不内联展开把下面内容顶走。

import { useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  ChevronLeft,
  ChevronRight,
  Inbox,
  Network,
  Search,
  Server,
  X,
} from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Button,
  Checkbox,
  DataTable,
  Input,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  SummaryStrip,
  cn,
  levelText,
  type DataTableColumn,
} from '@beacon/ui'
import type { HealthItem, MetricsSeriesPoint, ServerItem } from '@beacon/devmock'

import {
  ApiClientError,
  disableIdentity,
  fetchIdentities,
  fetchServers,
  setDraining,
  unbindIdentity,
} from '../../api/cluster'
import { fetchHealthList, fetchMetricsSeries } from '../../api/metrics'
import { LEVEL_META, badgeOf } from './health-level'
import ReasonDialog from './reason-dialog'

const PAGE_SIZE = 15

// 行动作意图
type RowAction =
  | { kind: 'disable'; row: ServerItem }
  | { kind: 'unbind'; row: ServerItem }
  | { kind: 'draining'; row: ServerItem; next: boolean }

interface AssetsPanelProps {
  namespaceId?: number
  // 查看健康详情（父级用右侧抽屉承载）
  onViewHealth: (serverId: string) => void
  // 打开注册待确认抽屉
  onOpenPending: () => void
  // 待确认数（吸顶入口徽标）
  pendingCount: number
}

export default function AssetsPanel({ namespaceId, onViewHealth, onOpenPending, pendingCount }: AssetsPanelProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [keyword, setKeyword] = useState('')
  const [kind, setKind] = useState<string>('all')
  const [assigned, setAssigned] = useState<string>('all')
  const [page, setPage] = useState(1)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [action, setAction] = useState<RowAction | null>(null)
  const [errorText, setErrorText] = useState<string | null>(null)

  const query = useQuery({
    queryKey: ['servers', 'assets', namespaceId, keyword, kind, assigned, page],
    queryFn: () =>
      fetchServers({
        namespaceId,
        keyword: keyword.trim() === '' ? undefined : keyword.trim(),
        kind: kind === 'all' ? undefined : kind,
        assigned: assigned === 'all' ? undefined : assigned === 'yes',
        page,
        pageSize: PAGE_SIZE,
      }),
    placeholderData: keepPreviousData,
  })

  // 身份端点按 identityId 定位，server 列表只给 serverId，故拉一份身份表建 serverId→identityId 映射。
  const identitiesQuery = useQuery({
    queryKey: ['identities', 'by-server', namespaceId],
    queryFn: () => fetchIdentities({ namespaceId, pageSize: 1000 }),
  })
  const identityIdOf = (row: ServerItem): string | null =>
    identitiesQuery.data?.items.find(
      (item) => item.serverId === row.serverId && item.namespaceId === row.namespaceId,
    )?.identityId ?? null

  // 健康视图列表：serverId → 健康分/等级/可调度/不可调度原因，供列表行直显基础健康信息（一眼可见，不必点开详情）。
  // 一次全量拉取（huge 场景 1200+ 台仍在单页上限内），避免逐行查详情的 N+1。
  const healthQuery = useQuery({
    queryKey: ['health', 'list', namespaceId],
    queryFn: () => fetchHealthList({ namespaceId, pageSize: 2000 }),
  })
  const healthByServer = useMemo(() => {
    const map = new Map<string, HealthItem>()
    for (const item of healthQuery.data?.items ?? []) {
      map.set(item.serverId, item)
    }
    return map
  }, [healthQuery.data])

  // 当前页各服的最新指标点（TPS / CPU / 在线人数）：一次请求带上整页 serverId，避免逐行 N+1。
  const pageServerIds = useMemo(
    () => (query.data?.items ?? []).map((row) => row.serverId).join(','),
    [query.data],
  )
  const seriesQuery = useQuery({
    queryKey: ['servers', 'latest-metrics', pageServerIds],
    queryFn: () => fetchMetricsSeries({ serverId: pageServerIds, step: 60 }),
    enabled: pageServerIds !== '',
  })
  const latestMetricsByServer = useMemo(() => {
    const map = new Map<string, MetricsSeriesPoint>()
    for (const series of seriesQuery.data?.series ?? []) {
      const last = series.points.at(-1)
      if (last) {
        map.set(series.serverId, last)
      }
    }
    return map
  }, [seriesQuery.data])

  const total = query.data?.total ?? 0
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['servers'] })

  const disableMutation = useMutation({
    mutationFn: ({ row, reason }: { row: ServerItem; reason: string }) => {
      const identityId = identityIdOf(row)
      if (identityId === null) {
        return Promise.reject(new ApiClientError(404, 'identity_not_found', '未找到该服务器的绑定身份'))
      }
      return disableIdentity(identityId, reason)
    },
    onSuccess: async () => {
      await invalidate()
      setAction(null)
    },
    onError: (error) => {
      setErrorText(messageOf(error))
    },
  })
  const unbindMutation = useMutation({
    mutationFn: ({ row, reason }: { row: ServerItem; reason: string }) => {
      const identityId = identityIdOf(row)
      if (identityId === null) {
        return Promise.reject(new ApiClientError(404, 'identity_not_found', '未找到该服务器的绑定身份'))
      }
      return unbindIdentity(identityId, reason)
    },
    onSuccess: async () => {
      await invalidate()
      setAction(null)
    },
    onError: (error) => {
      setErrorText(messageOf(error))
    },
  })
  const drainingMutation = useMutation({
    mutationFn: ({ row, reason, next }: { row: ServerItem; reason: string; next: boolean }) =>
      setDraining(row.serverId, next, reason),
    onSuccess: async () => {
      await invalidate()
      setAction(null)
    },
    onError: (error) => {
      setErrorText(messageOf(error))
    },
  })

  const toggleRow = (serverId: string) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(serverId)) {
        next.delete(serverId)
      } else {
        next.add(serverId)
      }
      return next
    })
  }

  const columns = useMemo<DataTableColumn<ServerItem>[]>(
    () => [
      {
        header: '',
        headClassName: 'w-8',
        cell: (row) => (
          <Checkbox
            checked={selected.has(row.serverId)}
            onCheckedChange={() => {
              toggleRow(row.serverId)
            }}
            aria-label={`选择 ${row.serverId}`}
            // 行整体点击已用于打开详情，勾选框自己吞掉事件避免误触发详情
            onClick={(e) => {
              e.stopPropagation()
            }}
          />
        ),
      },
      {
        header: t('cluster.servers.columns.serverId'),
        cell: (row) => {
          const isProxy = row.kind === 'proxy'
          return (
            <span className="flex items-center gap-2 font-mono font-semibold text-ink-1">
              <span
                className={cn(
                  'grid size-5 place-items-center rounded-md',
                  isProxy ? 'bg-brand-100 text-brand-600' : 'bg-brand-50 text-brand',
                )}
                aria-hidden
              >
                {isProxy ? <Network className="size-3" /> : <Server className="size-3" />}
              </span>
              {row.serverId}
              {row.isDefaultEntry && <Badge variant="brand">{t('cluster.zones.tree.defaultEntry')}</Badge>}
              {row.draining && <Badge variant="warn">{t('cluster.zones.tree.draining')}</Badge>}
            </span>
          )
        },
      },
      {
        header: t('cluster.servers.columns.kind'),
        cell: (row) => (
          <Badge variant={row.kind === 'proxy' ? 'brand' : 'secondary'} className="gap-1">
            {row.kind === 'proxy' ? <Network className="size-3" /> : <Server className="size-3" />}
            {t(`cluster.servers.kind.${row.kind}`)}
          </Badge>
        ),
      },
      {
        header: t('cluster.servers.columns.zone'),
        cell: (row) =>
          row.assigned ? (
            <span className="rounded-md border border-border-strong bg-surface-2 px-1.5 py-0.5 text-[11px] text-ink-3">
              {row.kind === 'backend'
                ? `${row.regionName ?? '-'} / ${row.zoneName ?? '-'}`
                : (row.bcClusterName ?? '-')}
            </span>
          ) : (
            <Badge variant="off">{t('cluster.servers.assets.assignedNo')}</Badge>
          ),
      },
      {
        header: t('cluster.servers.columns.health'),
        cell: (row) => {
          const health = healthByServer.get(row.serverId)
          const level = health ? (LEVEL_META[health.level] ?? 'warn') : 'warn'
          // 不可调度原因摘要：取首条原因码译文，多条时补「+N」
          const reasonSummary =
            health && health.reasons.length > 0
              ? t(`cluster.servers.schedReason.${health.reasons[0]}`, health.reasons[0]) +
                (health.reasons.length > 1 ? ` +${String(health.reasons.length - 1)}` : '')
              : null
          return (
            <div className="flex flex-wrap items-center gap-1.5">
              {/* 在线 / 失联 */}
              {row.online ? (
                <Badge variant="ok" className="gap-1.5">
                  <span className="size-1.5 rounded-full bg-current" />
                  {t('cluster.servers.summary.online')}
                </Badge>
              ) : (
                <Badge variant="crit" className="gap-1.5">
                  <span className="size-1.5 rounded-full bg-current" />
                  {t('cluster.servers.health.lost')}
                </Badge>
              )}
              {/* 健康分 + 等级（一眼可见，不必点开详情） */}
              {health && (
                <span className="flex items-center gap-1">
                  <span className={cn('text-[12px] font-semibold tnum', levelText(level))}>{health.score}</span>
                  <Badge variant={badgeOf(level)} className="gap-1">
                    {t(`cluster.servers.health.level_${health.level}`)}
                  </Badge>
                </span>
              )}
              {/* 可调度状态按例外呈现：仅不可调度时直显原因摘要，可调度不加多余药丸 */}
              {health && !health.schedulable && (
                <Badge variant="warn" title={health.reasons.join(', ')}>
                  {t('cluster.servers.health.notSchedulable')}
                  {reasonSummary != null && ` · ${reasonSummary}`}
                </Badge>
              )}
            </div>
          )
        },
      },
      {
        header: t('cluster.servers.columns.metrics'),
        cell: (row) => {
          const point = latestMetricsByServer.get(row.serverId)
          // 失联或指标未就绪时给占位，不展示误导性的旧数值
          if (!row.online || !point) {
            return <span className="text-ink-4">—</span>
          }
          return (
            <span className="tnum flex items-center gap-2.5 text-[12px] whitespace-nowrap">
              {/* TPS 只对子服有意义，代理不显示 */}
              {row.kind === 'backend' && (
                <span className="text-ink-4">
                  {t('cluster.servers.metrics.tps')}{' '}
                  <span className="font-semibold text-ink-2">{point.tpsAvg}</span>
                </span>
              )}
              <span className="text-ink-4">
                {t('cluster.servers.metrics.cpu')}{' '}
                <span className="font-semibold text-ink-2">{point.cpuPctAvg}%</span>
              </span>
              <span className="font-semibold text-ink-2">
                {t('cluster.servers.metrics.players', { count: point.onlineAvg })}
              </span>
            </span>
          )
        },
      },
      {
        header: t('cluster.servers.columns.actions'),
        headClassName: 'text-right',
        className: 'text-right',
        cell: (row) => (
          <div className="flex flex-wrap justify-end gap-1.5" onClick={(e) => { e.stopPropagation() }}>
            <Button size="sm" variant="ghost" onClick={() => { onViewHealth(row.serverId) }}>
              {t('cluster.servers.actions.viewHealth')}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                setErrorText(null)
                setAction({ kind: 'disable', row })
              }}
            >
              {t('cluster.servers.actions.disable')}
            </Button>
            {row.kind === 'backend' && row.assigned && (
              <Button
                size="sm"
                variant="ghost"
                onClick={() => {
                  setErrorText(null)
                  setAction({ kind: 'draining', row, next: !row.draining })
                }}
              >
                {row.draining
                  ? t('cluster.servers.actions.stopDraining')
                  : t('cluster.servers.actions.startDraining')}
              </Button>
            )}
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                setErrorText(null)
                setAction({ kind: 'unbind', row })
              }}
            >
              {t('cluster.servers.actions.unbind')}
            </Button>
          </div>
        ),
      },
    ],
    [t, selected, onViewHealth, healthByServer, latestMetricsByServer],
  )

  const active = action?.row ?? null
  const dialogConfig = action ? dialogConfigOf(action, t) : null

  // 紧凑 KPI 一行：总数 / 未分配 / 待确认（不占大块，语义色提示）
  const summaryItems = useMemo(
    () => [
      { label: t('cluster.servers.summary.total'), value: total, tone: 'default' as const },
      {
        label: t('cluster.servers.summary.pending'),
        value: pendingCount,
        tone: pendingCount > 0 ? ('warning' as const) : ('muted' as const),
      },
    ],
    [t, total, pendingCount],
  )

  return (
    <section className="grid gap-3.5">
      <SummaryStrip items={summaryItems} />

      <div className="grid gap-0 rounded-xl border border-border bg-card shadow-card">
        {/* 吸顶筛选/操作条：keyword + 类型 + 分配状态 + 待确认入口。列表滚动时保持可见。 */}
        <div className="sticky top-0 z-10 flex flex-wrap items-center gap-2 rounded-t-xl border-b border-border bg-card/95 px-4 py-3 backdrop-blur supports-backdrop-filter:bg-card/80">
          <span className="mr-1 flex items-center gap-2 text-[13px] font-semibold text-ink-1">
            <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
              <Server className="size-[15px]" />
            </span>
            {t('cluster.servers.assets.title')}
          </span>
          <div className="relative">
            <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-ink-4" />
            <Input
              aria-label={t('cluster.servers.assets.keyword')}
              placeholder={t('cluster.servers.assets.keyword')}
              value={keyword}
              onChange={(e) => {
                setKeyword(e.target.value)
                setPage(1)
              }}
              className="w-48 pl-8"
            />
          </div>
          <Select
            value={kind}
            onValueChange={(value) => {
              setKind(value)
              setPage(1)
            }}
          >
            <SelectTrigger className="w-28" aria-label={t('cluster.servers.assets.filterKind')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t('cluster.servers.assets.filterKind')}</SelectItem>
              <SelectItem value="proxy">{t('cluster.servers.kind.proxy')}</SelectItem>
              <SelectItem value="backend">{t('cluster.servers.kind.backend')}</SelectItem>
            </SelectContent>
          </Select>
          <Select
            value={assigned}
            onValueChange={(value) => {
              setAssigned(value)
              setPage(1)
            }}
          >
            <SelectTrigger className="w-28" aria-label={t('cluster.servers.assets.filterAssigned')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t('cluster.servers.assets.filterAssigned')}</SelectItem>
              <SelectItem value="yes">{t('cluster.servers.assets.assignedYes')}</SelectItem>
              <SelectItem value="no">{t('cluster.servers.assets.assignedNo')}</SelectItem>
            </SelectContent>
          </Select>

          {/* 待确认入口：收敛到吸顶条，点开在抽屉里处理，不占主列表版面 */}
          <Button variant="outline" size="sm" className="ml-auto gap-1.5" onClick={onOpenPending}>
            <Inbox className="size-3.5" />
            {t('cluster.servers.pending.title')}
            {pendingCount > 0 && (
              <Badge variant="warn" className="tnum">
                {pendingCount}
              </Badge>
            )}
          </Button>
        </div>

        {/* 批量选择集操作条：紧随筛选条，选择集非空才出现 */}
        {selected.size > 0 && (
          <div className="flex items-center gap-3 border-b border-brand-100 bg-brand-50 px-4 py-2 text-[12.5px] text-brand-600">
            <span className="font-medium">{t('cluster.servers.selection.selected', { count: selected.size })}</span>
            <Button
              size="sm"
              variant="outline"
              className="ml-auto gap-1"
              onClick={() => {
                setSelected(new Set())
              }}
            >
              <X className="size-3" />
              {t('cluster.servers.selection.clear')}
            </Button>
          </div>
        )}

        {/* 列表区：自身滚动（max-height），页面整体高度可控，1000+ 台亦不无限增高 */}
        <div className="max-h-[calc(100vh-20rem)] overflow-y-auto px-4 pt-2 pb-1">
          <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
            <DataTable
              columns={columns}
              rows={query.data?.items}
              rowKey={(row) => String(row.id)}
              emptyText={t('cluster.servers.assets.empty')}
              density="compact"
              onRowClick={(row) => { onViewHealth(row.serverId) }}
            />
          </AsyncSection>
        </div>

        {/* 服务端分页控件（吸底于卡片） */}
        {total > PAGE_SIZE && (
          <div className="flex items-center justify-end gap-2 border-t border-border px-4 py-2 text-[12px] text-ink-3">
            <span className="tnum">
              第 {page} / {pageCount} 页 · 共 {total} 台
            </span>
            <Button
              size="sm"
              variant="outline"
              className="gap-0.5"
              disabled={page <= 1}
              onClick={() => {
                setPage((p) => Math.max(1, p - 1))
              }}
            >
              <ChevronLeft className="size-3" />
              上一页
            </Button>
            <Button
              size="sm"
              variant="outline"
              className="gap-0.5"
              disabled={page >= pageCount}
              onClick={() => {
                setPage((p) => Math.min(pageCount, p + 1))
              }}
            >
              下一页
              <ChevronRight className="size-3" />
            </Button>
          </div>
        )}
      </div>

      {/* 行内写操作确认弹窗（原因必填） */}
      {dialogConfig && action && active && (
        <ReasonDialog
          open
          onOpenChange={(open) => {
            if (!open) {
              setAction(null)
            }
          }}
          title={dialogConfig.title}
          description={dialogConfig.description}
          confirmLabel={dialogConfig.confirmLabel}
          impacts={[`serverId ${active.serverId}`]}
          pending={disableMutation.isPending || unbindMutation.isPending || drainingMutation.isPending}
          errorText={errorText}
          onConfirm={(reason) => {
            const current = action
            if (current.kind === 'disable') {
              disableMutation.mutate({ row: current.row, reason })
            } else if (current.kind === 'unbind') {
              unbindMutation.mutate({ row: current.row, reason })
            } else {
              drainingMutation.mutate({ row: current.row, reason, next: current.next })
            }
          }}
        />
      )}
    </section>
  )
}

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

function dialogConfigOf(
  action: RowAction,
  t: (key: string) => string,
): { title: string; description: string; confirmLabel: string } {
  if (action.kind === 'disable') {
    return {
      title: t('cluster.servers.confirm.disableTitle'),
      description: t('cluster.servers.confirm.disableDesc'),
      confirmLabel: t('cluster.servers.actions.disable'),
    }
  }
  if (action.kind === 'unbind') {
    return {
      title: t('cluster.servers.confirm.unbindTitle'),
      description: t('cluster.servers.confirm.unbindDesc'),
      confirmLabel: t('cluster.servers.actions.unbind'),
    }
  }
  return {
    title: t('cluster.servers.confirm.drainingTitle'),
    description: t('cluster.servers.confirm.drainingDesc'),
    confirmLabel: action.next
      ? t('cluster.servers.actions.startDraining')
      : t('cluster.servers.actions.stopDraining'),
  }
}
