// 归档与清理块：目标库与各域水位总览 + 创建归档任务（试运行 / 执行，确认弹窗）+ 任务列表（详情 / 重试 / 取消）。
import { useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  AsyncSection,
  Badge,
  Button,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
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
import type { ArchiveJob } from '@beacon/devmock'

import {
  ApiClientError,
  cancelArchiveJob,
  createArchiveJob,
  fetchArchiveJobs,
  fetchArchiveOverview,
  retryArchiveJob,
} from '../../api/system'
import { formatCount, formatIso } from '../../features/system/format'
import ArchiveDetailSheet from './archive-detail-sheet'

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
  const [detailId, setDetailId] = useState<number | null>(null)
  const [confirm, setConfirm] = useState<ConfirmAction | null>(null)
  const [error, setError] = useState<string | null>(null)

  const overviewQuery = useQuery({
    queryKey: ['archive', 'overview'],
    queryFn: fetchArchiveOverview,
  })

  const jobsQuery = useQuery({
    queryKey: ['archive', 'jobs', statusFilter, page],
    queryFn: () =>
      fetchArchiveJobs({
        status: statusFilter === 'all' ? undefined : statusFilter,
        page,
        pageSize: PAGE_SIZE,
      }),
    placeholderData: keepPreviousData,
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
          <Badge variant="outline">
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
          <Badge variant={statusTone(row.status)}>{t(`system.settings.archive.status${statusSuffix(row.status)}`)}</Badge>
        ),
      },
      { header: t('system.settings.archive.operator'), cell: (row) => row.operator },
      { header: t('system.settings.archive.createdAt'), cell: (row) => formatIso(row.createdAt) },
      {
        header: t('system.settings.archive.jobActions'),
        cell: (row) => (
          <div className="flex flex-wrap gap-1.5">
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                setDetailId(row.id)
              }}
            >
              {t('system.settings.archive.detail')}
            </Button>
            {row.status === 'failed' && (
              <Button
                size="sm"
                variant="ghost"
                onClick={() => {
                  setError(null)
                  setConfirm({ kind: 'retry', job: row })
                }}
              >
                {t('system.settings.archive.retry')}
              </Button>
            )}
            {(row.status === 'pending' || row.status === 'running') && (
              <Button
                size="sm"
                variant="ghost"
                onClick={() => {
                  setError(null)
                  setConfirm({ kind: 'cancel', job: row })
                }}
              >
                {t('system.settings.archive.cancel')}
              </Button>
            )}
          </div>
        ),
      },
    ],
    [t],
  )

  const overview = overviewQuery.data
  const confirmConfig = confirm ? confirmConfigOf(confirm, t) : null

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <div>
          <CardTitle className="text-base">{t('system.settings.archive.title')}</CardTitle>
          <p className="mt-1 text-sm text-muted-foreground">{t('system.settings.archive.desc')}</p>
        </div>
        <div className="flex gap-2">
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
      </CardHeader>
      <CardContent className="grid gap-4">
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
              <div className="flex flex-wrap items-center gap-2 text-sm">
                <span className="text-muted-foreground">{t('system.settings.archive.target')}</span>
                <Badge variant="outline">
                  {overview.target.mode === 'external'
                    ? t('system.settings.archive.targetExternal')
                    : t('system.settings.archive.targetSameInstance')}
                </Badge>
                <span className="font-mono text-xs">{overview.target.dsnMasked}</span>
                <Badge variant={overview.target.reachable ? 'secondary' : 'destructive'}>
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
                          <span className="text-amber-600">{formatCount(d.expiredRows)}</span>
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

        {/* 任务列表 */}
        <div className="grid gap-2">
          <div className="flex items-center justify-between">
            <p className="text-sm font-medium">{t('system.settings.archive.jobs')}</p>
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
          <AsyncSection
            isLoading={jobsQuery.isLoading}
            isError={jobsQuery.isError}
            error={jobsQuery.error}
            skeleton={<TableSkeleton columns={7} rows={4} />}
          >
            <DataTable
              columns={columns}
              rows={jobsQuery.data?.items}
              rowKey={(row) => String(row.id)}
              emptyText={t('system.settings.archive.jobsEmpty')}
              density="compact"
            />
          </AsyncSection>
          {total > PAGE_SIZE && (
            <div className="flex items-center justify-end gap-2 text-sm text-muted-foreground">
              <span>{t('system.settings.archive.jobsHint', { page, pageCount, total })}</span>
              <Button
                size="sm"
                variant="outline"
                disabled={page <= 1}
                onClick={() => {
                  setPage((p) => Math.max(1, p - 1))
                }}
              >
                {t('system.common.prev')}
              </Button>
              <Button
                size="sm"
                variant="outline"
                disabled={page >= pageCount}
                onClick={() => {
                  setPage((p) => Math.min(pageCount, p + 1))
                }}
              >
                {t('system.common.next')}
              </Button>
            </div>
          )}
        </div>
      </CardContent>

      {/* 任务详情抽屉 */}
      <ArchiveDetailSheet
        jobId={detailId}
        onOpenChange={(open) => {
          if (!open) {
            setDetailId(null)
          }
        }}
      />

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
      {error !== null && <p className="px-6 pb-4 text-sm text-destructive">{error}</p>}
    </Card>
  )
}

function statusTone(status: ArchiveJob['status']): 'secondary' | 'outline' | 'destructive' {
  if (status === 'succeeded') {
    return 'secondary'
  }
  if (status === 'failed') {
    return 'destructive'
  }
  return 'outline'
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
