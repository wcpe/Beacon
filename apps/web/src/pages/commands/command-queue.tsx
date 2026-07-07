// 命令实时队列：仅 pending / fetched 在途命令，展示已等待时长；5 秒轮询刷新。
// 只读元数据，永不展示命令 payload / 回执明文。

import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  AsyncSection,
  Badge,
  DataTable,
  SectionHeader,
  TableSkeleton,
  type DataTableColumn,
} from '@beacon/ui'
import type { CommandItem } from '@beacon/devmock'

import { fetchCommands } from '../../api/observability'

const REFETCH_MS = 5000

export default function CommandQueue() {
  const { t } = useTranslation()

  // 分别拉 pending / fetched（Legacy 端点单值 status 过滤），合并成在途队列
  const pendingQuery = useQuery({
    queryKey: ['commands', 'queue', 'pending'],
    queryFn: () => fetchCommands({ status: 'pending', size: 50 }),
    refetchInterval: REFETCH_MS,
  })
  const fetchedQuery = useQuery({
    queryKey: ['commands', 'queue', 'fetched'],
    queryFn: () => fetchCommands({ status: 'fetched', size: 50 }),
    refetchInterval: REFETCH_MS,
  })

  const rows = useMemo<CommandItem[]>(() => {
    const merged = [...(pendingQuery.data?.items ?? []), ...(fetchedQuery.data?.items ?? [])]
    return merged.sort((a, b) => b.ageSeconds - a.ageSeconds)
  }, [pendingQuery.data, fetchedQuery.data])

  const columns = useMemo<DataTableColumn<CommandItem>[]>(
    () => [
      { header: t('observability.commands.columns.commandId'), cell: (row) => row.commandId },
      {
        header: t('observability.commands.columns.serverId'),
        cell: (row) => <span className="font-mono text-xs">{row.serverId}</span>,
      },
      { header: t('observability.commands.columns.type'), cell: (row) => row.type },
      {
        header: t('observability.commands.columns.status'),
        cell: (row) => (
          <Badge variant={row.status === 'pending' ? 'secondary' : 'outline'}>
            {t(`observability.commands.status.${row.status}`)}
          </Badge>
        ),
      },
      {
        header: t('observability.commands.columns.age'),
        cell: (row) => t('observability.commands.ageSeconds', { count: row.ageSeconds }),
      },
    ],
    [t],
  )

  return (
    <section className="grid gap-3">
      <SectionHeader title={t('observability.commands.queueTitle')} />
      <AsyncSection
        isLoading={pendingQuery.isLoading || fetchedQuery.isLoading}
        isError={pendingQuery.isError || fetchedQuery.isError}
        error={pendingQuery.error ?? fetchedQuery.error}
        skeleton={<TableSkeleton columns={columns.length} rows={4} />}
      >
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(row) => String(row.commandId)}
          emptyText={t('observability.commands.queueEmpty')}
          density="compact"
        />
      </AsyncSection>
    </section>
  )
}
