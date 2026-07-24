// 命令实时队列：仅 pending / fetched 在途命令，展示已等待时长；5 秒轮询刷新。
// 只读元数据，永不展示命令 payload / 回执明文。行可点开右侧详情面板。

import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Radio } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  DataTable,
  SectionHeader,
  TableSkeleton,
  type DataTableColumn,
} from '@beacon/ui'
import type { CommandItem } from '@beacon/contracts'

import { fetchCommands } from '../../api/observability'
import {
  filterItemsByEnvCodes,
  useEnvNamespaceCodes,
} from '../../features/env/use-env-scope'
import { commandTypeLabel } from '../../features/observability/command-labels'

const REFETCH_MS = 5000

interface CommandQueueProps {
  // 点击命令行看详情（父级用右侧非模态面板承载）
  onView: (item: CommandItem) => void
  // 当前选中命令 id（高亮用）
  selectedId: number | null
}

export default function CommandQueue({ onView, selectedId }: CommandQueueProps) {
  const { t } = useTranslation()
  // FR-178：在途队列跟随顶栏 env
  const envCodes = useEnvNamespaceCodes()
  const apiNamespace = envCodes !== null && envCodes.length === 1 ? envCodes[0] : undefined

  // 分别拉 pending / fetched（Legacy 端点单值 status 过滤），合并成在途队列
  const pendingQuery = useQuery({
    queryKey: ['commands', 'queue', 'pending', apiNamespace, envCodes],
    queryFn: () => fetchCommands({ status: 'pending', namespace: apiNamespace, size: 50 }),
    refetchInterval: REFETCH_MS,
  })
  const fetchedQuery = useQuery({
    queryKey: ['commands', 'queue', 'fetched', apiNamespace, envCodes],
    queryFn: () => fetchCommands({ status: 'fetched', namespace: apiNamespace, size: 50 }),
    refetchInterval: REFETCH_MS,
  })

  const rows = useMemo<CommandItem[]>(() => {
    const merged = [...(pendingQuery.data?.items ?? []), ...(fetchedQuery.data?.items ?? [])]
    const scoped =
      envCodes === null || envCodes.length === 1 ? merged : filterItemsByEnvCodes(merged, envCodes)
    return scoped.sort((a, b) => b.ageSeconds - a.ageSeconds)
  }, [pendingQuery.data, fetchedQuery.data, envCodes])

  const columns = useMemo<DataTableColumn<CommandItem>[]>(
    () => [
      {
        header: t('observability.commands.columns.commandId'),
        cell: (row) => <span className="font-mono text-xs text-ink-2">{row.commandId}</span>,
      },
      {
        header: t('observability.commands.columns.serverId'),
        cell: (row) => <span className="font-mono text-xs text-ink-2">{row.serverId}</span>,
      },
      {
        header: t('observability.commands.columns.type'),
        cell: (row) => <span className="text-ink-2">{commandTypeLabel(t, row.type)}</span>,
      },
      {
        header: t('observability.commands.columns.status'),
        cell: (row) => (
          <Badge variant={row.status === 'pending' ? 'warn' : 'brand'}>
            {t(`observability.commands.status.${row.status}`)}
          </Badge>
        ),
      },
      {
        header: t('observability.commands.columns.age'),
        cell: (row) => (
          <span className="tabular-nums text-ink-2">
            {t('observability.commands.ageSeconds', { count: row.ageSeconds })}
          </span>
        ),
      },
    ],
    [t],
  )

  return (
    <section className="grid gap-3">
      <SectionHeader
        icon={<Radio className="size-4 text-brand" />}
        title={t('observability.commands.queueTitle')}
        count={rows.length > 0 ? t('observability.common.total', { count: rows.length }) : undefined}
      />
      <AsyncSection
        isLoading={pendingQuery.isLoading || fetchedQuery.isLoading}
        isError={pendingQuery.isError || fetchedQuery.isError}
        error={pendingQuery.error ?? fetchedQuery.error}
        skeleton={<TableSkeleton columns={columns.length} rows={4} />}
      >
        <div className="max-h-[16rem] overflow-y-auto rounded-xl border border-border bg-card shadow-card">
          <DataTable
            columns={columns}
            rows={rows}
            rowKey={(row) => String(row.commandId)}
            emptyText={t('observability.commands.queueEmpty')}
            density="compact"
            onRowClick={onView}
            rowClassName={(row) => (row.commandId === selectedId ? 'bg-brand-50/60' : undefined)}
          />
        </div>
      </AsyncSection>
    </section>
  )
}
