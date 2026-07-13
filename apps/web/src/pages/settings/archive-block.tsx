// 归档与清理块：目标库与各域水位总览 + 创建归档任务（试运行 / 执行，确认弹窗）+ 任务列表（详情 / 重试 / 取消）。
import { useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Archive, Database } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Button,
  DataTable,
  DestructiveConfirmDialog,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
  TableSkeleton,
  type DataTableColumn,
} from '@beacon/ui'
import type { ArchiveJob } from '@beacon/contracts'

import {
  ApiClientError,
  cancelArchiveJob,
  createArchiveJob,
  fetchArchiveJobs,
  fetchArchiveOverview,
  retryArchiveJob,
} from '../../api/system'
import { formatCount, formatIso } from '../../features/system/format'
import { ARCHIVE_POLL_MS, hasActiveArchiveJob } from '../../features/system/archive-status'
import ListCard from '../../features/shared/list-card'
import MasterDetail from '../../features/shared/master-detail'
import Pager from '../../features/observability/pager'
import ArchiveDetailPanel from './archive-detail-panel'

const PAGE_SIZE = 10

// 二次确认意图
type ConfirmAction =
  | { kind: 'dryRun' }
  | { kind: 'execute' }
  | { kind: 'retry'; job: ArchiveJob }
  | { kind: 'cancel'; job: ArchiveJob }

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

function statusSuffix(status: string): string {
  const camel = status.replace(/_([a-z])/g, (_, c: string) => c.toUpperCase())
  return camel.charAt(0).toUpperCase() + camel.slice(1)
}

export default function ArchiveBlock() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [page, setPage] = useState(1)
  const [statusFilter, setStatusFilter] = useState('all')
  const [selectedId, setSelectedId] = useState<number | null>(null)
  const [confirm, setConfirm] = useState<ConfirmAction | null>(null)
  const [error, setError] = useState<string | null>(null)

  const jobsQuery = useQuery({
    queryKey: ['archive', 'jobs', statusFilter, page],
    queryFn: () =>
      fetchArchiveJobs({
        status: statusFilter === 'all' ? undefined : statusFilter,
        page,
        pageSize: PAGE_SIZE,
      }),
    placeholderData: keepPreviousData,
    // 当前页存在进行中任务时轮询，全部终态即停（回调每轮基于最新数据重算）。
    refetchInterval: (query) => (hasActiveArchiveJob(query.state.data?.items) ? ARCHIVE_POLL_MS : false),
  })

  // 水位随归档删除变化：进行中任务期间跟随列表一起轮询，否则不轮询。
  const overviewQuery = useQuery({
    queryKey: ['archive', 'overview'],
    queryFn: fetchArchiveOverview,
    refetchInterval: hasActiveArchiveJob(jobsQuery.data?.items) ? ARCHIVE_POLL_MS : false,
  })

  const total = jobsQuery.data?.total ?? 0
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))

  const invalidate = async () => {
    await queryClient.invalidateQueries({ queryKey: ['archive'] })
  }

  const createMutation = useMutation({
    mutationFn: (mode: 'dry_run' | 'execute') => createArchiveJob({ mode }),
    onSuccess: async () => {
      await invalidate()
      setConfirm(null)
    },
    onError: (err) => {
      setError(messageOf(err))
    },
  })

  const retryMutation = useMutation({
    mutationFn: (job: ArchiveJob) => retryArchiveJob(job.id),
    onSuccess: async () => {
      await invalidate()
      setConfirm(null)
    },
    onError: (err) => {
      setError(messageOf(err))
    },
  })

  const cancelMutation = useMutation({
    mutationFn: (job: ArchiveJob) => cancelArchiveJob(job.id),
    onSuccess: async () => {
      await invalidate()
      setConfirm(null)
    },
    onError: (err) => {
      setError(messageOf(err))
    },
  })

  const columns = useMemo<DataTableColumn<ArchiveJob>[]>(
    () => [
      { header: t('system.settings.archive.jobId'), cell: (row) => `#${String(row.id)}` },
      {
        header: t('system.settings.archive.mode'),
        cell: (row) => (
          <Badge variant={row.mode === 'dry_run' ? 'off' : 'brand'}>
            {row.mode === 'dry_run'
              ? t('system.settings.archive.modeDryRun')
              : t('system.settings.archive.modeExecute')}
          </Badge>
        ),
      },
      {
        header: t('system.settings.archive.trigger'),
        cell: (row) =>
          row.trigger === 'scheduled'
            ? t('system.settings.archive.triggerScheduled')
            : t('system.settings.archive.triggerManual'),
      },
      {
        header: t('system.settings.archive.status'),
        cell: (row) => (
          <Badge variant={statusTone(row.status)} className="gap-1.5">
            <span className="size-1.5 rounded-full bg-current" />
            {t(`system.settings.archive.status${statusSuffix(row.status)}`)}
          </Badge>
        ),
      },
      { header: t('system.settings.archive.operator'), cell: (row) => row.operator },
      { header: t('system.settings.archive.createdAt'), cell: (row) => formatIso(row.createdAt) },
    ],
    [t],
  )

  const overview = overviewQuery.data
  const confirmConfig = confirm ? confirmConfigOf(confirm, t) : null
  const jobs = jobsQuery.data?.items ?? []
  const selectedJob = jobs.find((j) => j.id === selectedId) ?? null

  const jobsToolbar = (
    <div className="flex items-center justify-between gap-2">
      <p className="text-[13px] font-semibold text-ink-1">{t('system.settings.archive.jobs')}</p>
      <Select
        value={statusFilter}
        onValueChange={(value) => {
          setStatusFilter(value)
          setPage(1)
        }}
      >
        <SelectTrigger className="w-32" aria-label={t('system.settings.archive.filterStatus')}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="all">{t('system.settings.archive.filterStatus')}</SelectItem>
          <SelectItem value="succeeded">{t('system.settings.archive.statusSucceeded')}</SelectItem>
          <SelectItem value="failed">{t('system.settings.archive.statusFailed')}</SelectItem>
          <SelectItem value="running">{t('system.settings.archive.statusRunning')}</SelectItem>
          <SelectItem value="cancelled">{t('system.settings.archive.statusCancelled')}</SelectItem>
        </SelectContent>
      </Select>
    </div>
  )

  return (
    <div className="grid gap-4 rounded-xl border border-border bg-card p-4 shadow-card">
      <div className="flex flex-row items-start justify-between gap-3">
        <div className="flex items-start gap-2.5">
          <span className="grid size-[26px] shrink-0 place-items-center rounded-lg bg-brand-50 text-brand" aria-hidden>
            <Archive className="size-[15px]" />
          </span>
          <div>
            <h3 className="text-[14px] font-semibold text-ink-1">{t('system.settings.archive.title')}</h3>
            <p className="mt-1 text-sm text-ink-3">{t('system.settings.archive.desc')}</p>
          </div>
        </div>
        <div className="flex shrink-0 gap-2">
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              setError(null)
              setConfirm({ kind: 'dryRun' })
            }}
          >
            {t('system.settings.archive.createDryRun')}
          </Button>
          <Button
            size="sm"
            onClick={() => {
              setError(null)
              setConfirm({ kind: 'execute' })
            }}
          >
            {t('system.settings.archive.createExecute')}
          </Button>
        </div>
      </div>
      <div className="grid gap-4">
        {/* 目标库 + 各域水位总览 */}
        <AsyncSection
          isLoading={overviewQuery.isLoading}
          isError={overviewQuery.isError}
          error={overviewQuery.error}
          loadingText={t('system.settings.archive.overviewFail')}
          skeleton={<TableSkeleton columns={5} rows={4} />}
        >
          {overview && (
            <>
              <div className="flex flex-wrap items-center gap-2 rounded-lg bg-surface-2 px-3 py-2.5 text-sm">
                <Database className="size-4 text-ink-4" aria-hidden />
                <span className="text-ink-3">{t('system.settings.archive.target')}</span>
                <Badge variant="brand">
                  {overview.target.mode === 'external'
                    ? t('system.settings.archive.targetExternal')
                    : t('system.settings.archive.targetSameInstance')}
                </Badge>
                <span className="font-mono text-xs text-ink-2">{overview.target.dsnMasked}</span>
                <Badge variant={overview.target.reachable ? 'ok' : 'crit'} className="gap-1.5">
                  <span className="size-1.5 rounded-full bg-current" />
                  {overview.target.reachable
                    ? t('system.settings.archive.reachable')
                    : t('system.settings.archive.unreachable')}
                </Badge>
              </div>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t('system.settings.archive.domain')}</TableHead>
                    <TableHead className="text-right">{t('system.settings.archive.retention')}</TableHead>
                    <TableHead className="text-right">{t('system.settings.archive.hotRows')}</TableHead>
                    <TableHead className="text-right">{t('system.settings.archive.archiveRows')}</TableHead>
                    <TableHead className="text-right">{t('system.settings.archive.expiredRows')}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {overview.domains.map((d) => (
                    <TableRow key={d.domain}>
                      <TableCell>{t(`system.settings.archiveDomain.${d.domain}`)}</TableCell>
                      <TableCell className="text-right tabular-nums">{d.retentionDays}</TableCell>
                      <TableCell className="text-right tabular-nums">{formatCount(d.hotRows)}</TableCell>
                      <TableCell className="text-right tabular-nums">{formatCount(d.archiveRows)}</TableCell>
                      <TableCell className="text-right tabular-nums">
                        {d.expiredRows > 0 ? (
                          <span className="font-semibold text-warn">{formatCount(d.expiredRows)}</span>
                        ) : (
                          formatCount(d.expiredRows)
                        )}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </>
          )}
        </AsyncSection>

        {/* 任务列表主从：主列（吸顶过滤 + 自区滚 + 吸底分页）+ 点行右侧非模态详情面板 */}
        <MasterDetail
          master={
            <ListCard
              toolbar={jobsToolbar}
              footer={
                total > PAGE_SIZE ? (
                  <Pager page={page} pageCount={pageCount} total={total} onPageChange={setPage} />
                ) : undefined
              }
            >
              <AsyncSection
                isLoading={jobsQuery.isLoading}
                isError={jobsQuery.isError}
                error={jobsQuery.error}
                skeleton={<TableSkeleton columns={columns.length} rows={4} />}
              >
                <DataTable
                  columns={columns}
                  rows={jobs}
                  rowKey={(row) => String(row.id)}
                  emptyText={t('system.settings.archive.jobsEmpty')}
                  density="compact"
                  onRowClick={(row) => {
                    setSelectedId(row.id)
                  }}
                  rowClassName={(row) => (row.id === selectedId ? 'bg-brand-50/60' : undefined)}
                />
              </AsyncSection>
            </ListCard>
          }
          detail={
            selectedJob ? (
              <ArchiveDetailPanel
                job={selectedJob}
                onRetry={(job) => {
                  setError(null)
                  setConfirm({ kind: 'retry', job })
                }}
                onCancel={(job) => {
                  setError(null)
                  setConfirm({ kind: 'cancel', job })
                }}
              />
            ) : null
          }
          detailTitle={t('system.settings.archive.detailTitle')}
          closeLabel={t('system.common.close')}
          onClose={() => {
            setSelectedId(null)
          }}
        />
      </div>

      {/* 统一二次确认（试运行 / 执行 / 重试 / 取消） */}
      {confirmConfig && (
        <DestructiveConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open) {
              setConfirm(null)
            }
          }}
          title={confirmConfig.title}
          description={confirmConfig.description}
          confirmLabel={confirmConfig.confirmLabel}
          cancelLabel={t('system.common.cancel')}
          pending={createMutation.isPending || retryMutation.isPending || cancelMutation.isPending}
          onConfirm={() => {
            const current = confirm
            if (current === null) {
              return
            }
            if (current.kind === 'dryRun') {
              createMutation.mutate('dry_run')
            } else if (current.kind === 'execute') {
              createMutation.mutate('execute')
            } else if (current.kind === 'retry') {
              retryMutation.mutate(current.job)
            } else {
              cancelMutation.mutate(current.job)
            }
          }}
        />
      )}
      {error !== null && <p className="text-sm text-crit">{error}</p>}
    </div>
  )
}

// 归档任务状态 → 语义药丸变体：成功绿 / 失败红 / 其他（运行 / 待处理 / 取消）中性。
function statusTone(status: ArchiveJob['status']): 'ok' | 'crit' | 'off' {
  if (status === 'succeeded') {
    return 'ok'
  }
  if (status === 'failed') {
    return 'crit'
  }
  return 'off'
}

function confirmConfigOf(
  action: ConfirmAction,
  t: (key: string) => string,
): { title: string; description: string; confirmLabel: string } {
  if (action.kind === 'dryRun') {
    return {
      title: t('system.settings.archive.confirmDryRunTitle'),
      description: t('system.settings.archive.confirmDryRunDesc'),
      confirmLabel: t('system.settings.archive.confirmDryRun'),
    }
  }
  if (action.kind === 'execute') {
    return {
      title: t('system.settings.archive.confirmExecuteTitle'),
      description: t('system.settings.archive.confirmExecuteDesc'),
      confirmLabel: t('system.settings.archive.confirmExecute'),
    }
  }
  if (action.kind === 'retry') {
    return {
      title: t('system.settings.archive.confirmRetryTitle'),
      description: t('system.settings.archive.confirmRetryDesc'),
      confirmLabel: t('system.settings.archive.confirmRetry'),
    }
  }
  return {
    title: t('system.settings.archive.confirmCancelTitle'),
    description: t('system.settings.archive.confirmCancelDesc'),
    confirmLabel: t('system.settings.archive.confirmCancel'),
  }
}
