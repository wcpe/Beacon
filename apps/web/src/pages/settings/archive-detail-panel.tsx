// 归档任务详情面板内容（非模态右侧列）：逐 item（域 × 表）的阶段、进度、删除行数与校验结果，
// + 重试（失败态）/ 取消（排队 / 进行中）操作。取代原「详情抽屉」的模态 Sheet。
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  AsyncSection,
  Badge,
  Button,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@beacon/ui'
import type { ArchiveJob, ArchiveJobItem } from '@beacon/contracts'

import { fetchArchiveJobDetail } from '../../api/system'
import { formatCount, formatIso } from '../../features/system/format'
import { ARCHIVE_POLL_MS, isActiveArchiveStatus } from '../../features/system/archive-status'

interface ArchiveDetailPanelProps {
  // 选中的任务行（提供状态与创建时间，避免二次等待明细）
  job: ArchiveJob
  // 请求重试 / 取消（打开二次确认模态）
  onRetry: (job: ArchiveJob) => void
  onCancel: (job: ArchiveJob) => void
}

export default function ArchiveDetailPanel({ job, onRetry, onCancel }: ArchiveDetailPanelProps) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['archive', 'job', job.id],
    queryFn: () => fetchArchiveJobDetail(job.id),
    // 任务进行中时轮询逐 item 进度，终态即停（优先用详情自身最新状态，回退到列表行状态）。
    refetchInterval: (q) =>
      isActiveArchiveStatus(q.state.data?.status ?? job.status) ? ARCHIVE_POLL_MS : false,
  })
  const detail = query.data

  const verifyBadge = (item: ArchiveJobItem) => {
    if (item.verifyPassed === true) {
      return <Badge variant="ok">{t('system.settings.archive.verifyPassed')}</Badge>
    }
    if (item.verifyPassed === false) {
      return <Badge variant="crit">{t('system.settings.archive.verifyFailed')}</Badge>
    }
    return <Badge variant="off">{t('system.settings.archive.verifyPending')}</Badge>
  }

  return (
    <div className="grid gap-3 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-[15px] font-semibold text-ink-1">#{job.id}</span>
        <Badge variant={statusTone(job.status)} className="gap-1.5">
          <span className="size-1.5 rounded-full bg-current" />
          {t(`system.settings.archive.status${statusSuffix(job.status)}`)}
        </Badge>
        <Badge variant={job.mode === 'dry_run' ? 'off' : 'brand'}>
          {job.mode === 'dry_run'
            ? t('system.settings.archive.modeDryRun')
            : t('system.settings.archive.modeExecute')}
        </Badge>
      </div>

      <div className="grid gap-1">
        <span className="text-xs text-ink-4">{t('system.settings.archive.createdAt')}</span>
        <span className="text-sm text-ink-1">{formatIso(job.createdAt)}</span>
      </div>

      {/* 重试 / 取消：仅在对应状态可操作，点击开二次确认模态 */}
      {(job.status === 'failed' || job.status === 'pending' || job.status === 'running') && (
        <div className="flex flex-wrap gap-2">
          {job.status === 'failed' && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                onRetry(job)
              }}
            >
              {t('system.settings.archive.retry')}
            </Button>
          )}
          {(job.status === 'pending' || job.status === 'running') && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                onCancel(job)
              }}
            >
              {t('system.settings.archive.cancel')}
            </Button>
          )}
        </div>
      )}

      {/* 逐域进度与校验明细 */}
      <div className="border-t border-border pt-3">
        <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
          {detail && (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('system.settings.archive.itemDomain')}</TableHead>
                  <TableHead>{t('system.settings.archive.itemPhase')}</TableHead>
                  <TableHead>{t('system.settings.archive.itemProgress')}</TableHead>
                  <TableHead>{t('system.settings.archive.itemVerify')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {detail.items.map((item) => (
                  <TableRow key={item.id}>
                    <TableCell>{t(`system.settings.archiveDomain.${item.domain}`)}</TableCell>
                    <TableCell>{t(`system.settings.archive.phase${phaseSuffix(item.phase)}`)}</TableCell>
                    <TableCell className="tabular-nums">
                      {formatCount(item.rowsCopied)} / {formatCount(item.rowsExpected)}
                    </TableCell>
                    <TableCell>{verifyBadge(item)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </AsyncSection>
      </div>
    </div>
  )
}

// 归档任务状态 → 语义药丸变体：成功绿 / 失败红 / 其他中性。
function statusTone(status: ArchiveJob['status']): 'ok' | 'crit' | 'off' {
  if (status === 'succeeded') {
    return 'ok'
  }
  if (status === 'failed') {
    return 'crit'
  }
  return 'off'
}

// 状态 / 阶段枚举 → i18n 键后缀（首字母大写驼峰）
function statusSuffix(status: string): string {
  return status.charAt(0).toUpperCase() + toCamel(status).slice(1)
}

function phaseSuffix(phase: string): string {
  return phase.charAt(0).toUpperCase() + toCamel(phase).slice(1)
}

function toCamel(value: string): string {
  return value.replace(/_([a-z])/g, (_, c: string) => c.toUpperCase())
}
