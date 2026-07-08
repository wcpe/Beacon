// 未分配篮：assigned=false 的 server 列表 + 批量首次分配。
// 选择集只允许同 kind（分配要求同 namespace、同 kind），换区中的 server 标注 pending。

import { useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Inbox, Network, Server } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Button,
  Checkbox,
  DataTable,
  cn,
  type DataTableColumn,
} from '@beacon/ui'
import type { AssignmentResult, ServerItem } from '@beacon/devmock'

import { ApiClientError, assignServers, fetchServers, fetchZoneTree } from '../../api/cluster'
import AssignDialog, { targetOptionsOf } from './assign-dialog'

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

export default function UnassignedBasket({ namespaceId }: { namespaceId: number }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set())
  const [assignOpen, setAssignOpen] = useState(false)
  const [errorText, setErrorText] = useState<string | null>(null)
  const [results, setResults] = useState<AssignmentResult[] | null>(null)

  const query = useQuery({
    queryKey: ['servers', 'unassigned', namespaceId],
    queryFn: () => fetchServers({ namespaceId, assigned: false, pageSize: 200 }),
    placeholderData: keepPreviousData,
  })
  const treeQuery = useQuery({
    queryKey: ['zone-tree', namespaceId],
    queryFn: () => fetchZoneTree(namespaceId),
    placeholderData: keepPreviousData,
  })

  const rows = query.data?.items ?? []
  // 已选中的 server 行（决定 kind 与分配目标）
  const selectedRows = useMemo(() => rows.filter((r) => selectedIds.has(r.id)), [rows, selectedIds])
  // 选择集的 kind：以首个选中项为准（无选中为 null），其余 kind 的行禁选
  const selectionKind: 'backend' | 'proxy' | null =
    selectedRows.length > 0 ? selectedRows[0].kind : null

  const assignMutation = useMutation({
    mutationFn: ({ targetId, isDefaultEntry }: { targetId: string; isDefaultEntry: boolean }) => {
      const kind = selectionKind === 'proxy' ? 'bc_cluster' : 'zone'
      return assignServers({
        serverIds: selectedRows.map((r) => r.id),
        target: { kind, id: Number.parseInt(targetId, 10) },
        isDefaultEntry,
      })
    },
    onSuccess: async (response) => {
      setResults(response.results)
      setSelectedIds(new Set())
      await queryClient.invalidateQueries({ queryKey: ['servers'] })
      await queryClient.invalidateQueries({ queryKey: ['zone-tree'] })
    },
    onError: (error) => {
      // 整批 409（含 rezone_required 逐台原因）：把结构化结果展示到弹窗
      if (error instanceof ApiClientError && error.status === 409) {
        setErrorText(error.message)
      } else {
        setErrorText(messageOf(error))
      }
    },
  })

  const toggle = (row: ServerItem) => {
    setSelectedIds((prev) => {
      const next = new Set(prev)
      if (next.has(row.id)) {
        next.delete(row.id)
      } else {
        next.add(row.id)
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
            checked={selectedIds.has(row.id)}
            // 已选其它 kind 时禁选跨 kind 行
            disabled={selectionKind !== null && row.kind !== selectionKind && !selectedIds.has(row.id)}
            onCheckedChange={() => {
              toggle(row)
            }}
            aria-label={`选择 ${row.serverId}`}
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
            </span>
          )
        },
      },
      {
        header: t('cluster.servers.columns.kind'),
        cell: (row) => <span className="text-ink-2">{t(`cluster.servers.kind.${row.kind}`)}</span>,
      },
      {
        header: t('cluster.servers.columns.status'),
        cell: (row) => (
          <div className="flex flex-wrap gap-1">
            {row.pendingZoneId !== null && (
              <Badge variant="warn" className="gap-1.5">
                <span className="size-1.5 rounded-full bg-current" />
                {t('cluster.servers.pending.rezoneHint')} → {row.pendingZoneName ?? ''}
              </Badge>
            )}
            {row.online ? (
              <Badge variant="ok" className="gap-1.5">
                <span className="size-1.5 rounded-full bg-current" />
                {t('cluster.servers.summary.online')}
              </Badge>
            ) : (
              <Badge variant="crit" className="gap-1.5">
                <span className="size-1.5 rounded-full bg-current" />
                lost
              </Badge>
            )}
          </div>
        ),
      },
    ],
    [t, selectedIds, selectionKind],
  )

  const options = targetOptionsOf(treeQuery.data, selectionKind === 'proxy' ? 'proxy' : 'backend')

  return (
    <section className="grid gap-3 rounded-xl border border-border bg-card p-4 shadow-card">
      <div className="flex items-center gap-2.5">
        <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
          <Inbox className="size-[15px]" />
        </span>
        <h2 className="text-[13px] font-semibold text-ink-1">{t('cluster.zones.basket.title')}</h2>
        <Button
          size="sm"
          className="ml-auto"
          disabled={selectedIds.size === 0}
          onClick={() => {
            setErrorText(null)
            setResults(null)
            setAssignOpen(true)
          }}
        >
          {t('cluster.zones.basket.assign')}
        </Button>
      </div>

      {selectedIds.size > 0 && (
        <div className="rounded-lg border border-brand-100 bg-brand-50 px-3 py-2 text-[12.5px] font-medium text-brand-600">
          {t('cluster.zones.basket.selected', { count: selectedIds.size })}
        </div>
      )}

      <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(row) => String(row.id)}
          emptyText={t('cluster.zones.basket.empty')}
          density="compact"
        />
      </AsyncSection>

      <AssignDialog
        open={assignOpen}
        onOpenChange={(open) => {
          setAssignOpen(open)
          if (!open) {
            setResults(null)
            setErrorText(null)
          }
        }}
        servers={selectedRows}
        kind={selectionKind === 'proxy' ? 'proxy' : 'backend'}
        options={options}
        pending={assignMutation.isPending}
        errorText={errorText}
        results={results}
        onConfirm={(targetId, isDefaultEntry) => {
          assignMutation.mutate({ targetId, isDefaultEntry })
        }}
      />
    </section>
  )
}
