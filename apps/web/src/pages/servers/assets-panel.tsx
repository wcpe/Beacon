// 服务器资产列表：类型 / 分配状态 / 小区筛选 + keyword 搜索 + 服务端分页，
// 行内动作（禁用 / 启用 / 解绑 / 排空 / 健康详情）+ 批量选择集顶部操作条。

import { useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  AsyncSection,
  Badge,
  Button,
  Checkbox,
  DataTable,
  Input,
  SectionHeader,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  SummaryStrip,
  type DataTableColumn,
} from '@beacon/ui'
import type { ServerItem } from '@beacon/devmock'

import {
  ApiClientError,
  disableIdentity,
  fetchIdentities,
  fetchServers,
  setDraining,
  unbindIdentity,
} from '../../api/cluster'
import ReasonDialog from './reason-dialog'

const PAGE_SIZE = 15

// 行动作意图
type RowAction =
  | { kind: 'disable'; row: ServerItem }
  | { kind: 'unbind'; row: ServerItem }
  | { kind: 'draining'; row: ServerItem; next: boolean }

interface AssetsPanelProps {
  namespaceId?: number
  onViewHealth: (serverId: string) => void
}

export default function AssetsPanel({ namespaceId, onViewHealth }: AssetsPanelProps) {
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
          />
        ),
      },
      {
        header: t('cluster.servers.columns.serverId'),
        cell: (row) => (
          <span className="flex items-center gap-1.5 font-mono">
            {row.serverId}
            {row.isDefaultEntry && <Badge variant="outline">{t('cluster.zones.tree.defaultEntry')}</Badge>}
            {row.draining && <Badge variant="secondary">{t('cluster.zones.tree.draining')}</Badge>}
          </span>
        ),
      },
      { header: t('cluster.servers.columns.kind'), cell: (row) => t(`cluster.servers.kind.${row.kind}`) },
      {
        header: t('cluster.servers.columns.zone'),
        cell: (row) =>
          row.assigned ? (
            <span>
              {row.kind === 'backend'
                ? `${row.regionName ?? '-'} / ${row.zoneName ?? '-'}`
                : (row.bcClusterName ?? '-')}
            </span>
          ) : (
            <Badge variant="outline">{t('cluster.servers.assets.assignedNo')}</Badge>
          ),
      },
      {
        header: t('cluster.servers.columns.health'),
        cell: (row) =>
          row.online ? (
            <Badge variant="secondary">{t('cluster.servers.summary.online')}</Badge>
          ) : (
            <Badge variant="destructive">lost</Badge>
          ),
      },
      {
        header: t('cluster.servers.columns.actions'),
        cell: (row) => (
          <div className="flex flex-wrap gap-1.5">
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
    [t, selected, onViewHealth],
  )

  const summaryItems = [
    { label: t('cluster.servers.summary.total'), value: total },
    {
      label: t('cluster.servers.summary.online'),
      value: query.data?.items.filter((s) => s.online).length ?? 0,
      tone: 'success' as const,
    },
    {
      label: t('cluster.servers.summary.unassigned'),
      value: query.data?.items.filter((s) => !s.assigned).length ?? 0,
      tone: 'warning' as const,
    },
  ]

  const active = action?.row ?? null
  const dialogConfig = action ? dialogConfigOf(action, t) : null

  return (
    <section className="grid gap-3">
      <SectionHeader title={t('cluster.servers.assets.title')} />
      <SummaryStrip items={summaryItems} />

      {/* 筛选条：keyword + 类型 + 分配状态 */}
      <div className="flex flex-wrap items-center gap-2">
        <Input
          aria-label={t('cluster.servers.assets.keyword')}
          placeholder={t('cluster.servers.assets.keyword')}
          value={keyword}
          onChange={(e) => {
            setKeyword(e.target.value)
            setPage(1)
          }}
          className="w-52"
        />
        <Select
          value={kind}
          onValueChange={(value) => {
            setKind(value)
            setPage(1)
          }}
        >
          <SelectTrigger className="w-32" aria-label={t('cluster.servers.assets.filterKind')}>
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
          <SelectTrigger className="w-32" aria-label={t('cluster.servers.assets.filterAssigned')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t('cluster.servers.assets.filterAssigned')}</SelectItem>
            <SelectItem value="yes">{t('cluster.servers.assets.assignedYes')}</SelectItem>
            <SelectItem value="no">{t('cluster.servers.assets.assignedNo')}</SelectItem>
          </SelectContent>
        </Select>
      </div>

      {/* 批量选择集顶部操作条 */}
      {selected.size > 0 && (
        <div className="flex items-center gap-3 rounded-md bg-secondary px-3 py-2 text-sm">
          <span>{t('cluster.servers.selection.selected', { count: selected.size })}</span>
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              setSelected(new Set())
            }}
          >
            {t('cluster.servers.selection.clear')}
          </Button>
        </div>
      )}

      <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
        <DataTable
          columns={columns}
          rows={query.data?.items}
          rowKey={(row) => String(row.id)}
          emptyText={t('cluster.servers.assets.empty')}
          density="compact"
        />
      </AsyncSection>

      {/* 服务端分页控件 */}
      {total > PAGE_SIZE && (
        <div className="flex items-center justify-end gap-2 text-sm text-muted-foreground">
          <span>
            第 {page} / {pageCount} 页 · 共 {total} 台
          </span>
          <Button
            size="sm"
            variant="outline"
            disabled={page <= 1}
            onClick={() => {
              setPage((p) => Math.max(1, p - 1))
            }}
          >
            上一页
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={page >= pageCount}
            onClick={() => {
              setPage((p) => Math.min(pageCount, p + 1))
            }}
          >
            下一页
          </Button>
        </div>
      )}

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
