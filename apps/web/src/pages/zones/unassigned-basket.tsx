// 未分配抽屉（Sheet）：assigned=false 的 server 列表 + 批量首次分配。默认收起，
// 由页面顶部「未分配 N」入口打开，不默认占版面。
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
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  cn,
  type DataTableColumn,
} from '@beacon/ui'
import type { AssignmentResult, ServerItem } from '@beacon/devmock'

import { ApiClientError, assignServers, fetchServers, fetchZoneTree } from '../../api/cluster'
import AssignDialog, { targetOptionsOf } from './assign-dialog'

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

interface UnassignedBasketProps {
  namespaceId: number
  // 抽屉开关（由页面顶部入口控制）
  open: boolean
  onOpenChange: (open: boolean) => void
}

export default function UnassignedBasket({ namespaceId, open, onOpenChange }: UnassignedBasketProps) {
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
        cell: (row) => (
          <Badge variant={row.kind === 'proxy' ? 'brand' : 'secondary'} className="gap-1">
            {row.kind === 'proxy' ? <Network className="size-3" /> : <Server className="size-3" />}
            {t(`cluster.servers.kind.${row.kind}`)}
          </Badge>
        ),
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
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="flex w-full flex-col gap-0 sm:max-w-xl">
        <SheetHeader>
          <SheetTitle className="flex items-center gap-2">
            <Inbox className="size-4 text-brand" />
            {t('cluster.zones.basket.title')}
          </SheetTitle>
          <SheetDescription>{t('cluster.zones.basket.sheetDesc')}</SheetDescription>
        </SheetHeader>

        {/* 操作条：选择集提示 + 批量分配 */}
        <div className="flex items-center gap-3 border-b border-border px-4 py-2.5">
          {selectedIds.size > 0 ? (
            <span className="text-[12.5px] font-medium text-brand-600">
              {t('cluster.zones.basket.selected', { count: selectedIds.size })}
            </span>
          ) : (
            <span className="text-[12.5px] text-ink-4">{t('cluster.zones.basket.selectHint')}</span>
          )}
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

        <div className="flex-1 overflow-y-auto px-4 py-3">
          <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
            <DataTable
              columns={columns}
              rows={rows}
              rowKey={(row) => String(row.id)}
              emptyText={t('cluster.zones.basket.empty')}
              density="compact"
              pageSize={20}
            />
          </AsyncSection>
        </div>

        <AssignDialog
          open={assignOpen}
          onOpenChange={(isOpen) => {
            setAssignOpen(isOpen)
            if (!isOpen) {
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
      </SheetContent>
    </Sheet>
  )
}
