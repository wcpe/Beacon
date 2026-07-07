// 归档任务详情抽屉：逐 item（域 × 表）的阶段、进度、删除行数与校验结果。
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  AsyncSection,
  Badge,
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@beacon/ui'
import type { ArchiveJobItem } from '@beacon/devmock'

import { fetchArchiveJobDetail } from '../../api/system'
import { formatCount } from '../../features/system/format'

interface ArchiveDetailSheetProps {
  jobId: number | null
  onOpenChange: (open: boolean) => void
}

export default function ArchiveDetailSheet({ jobId, onOpenChange }: ArchiveDetailSheetProps) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['archive', 'job', jobId],
    queryFn: () => fetchArchiveJobDetail(jobId ?? 0),
    enabled: jobId !== null,
  })
  const detail = query.data

  const verifyBadge = (item: ArchiveJobItem) => {
    if (item.verifyPassed === true) {
      return <Badge variant="secondary">{t('system.settings.archive.verifyPassed')}</Badge>
    }
    if (item.verifyPassed === false) {
      return <Badge variant="destructive">{t('system.settings.archive.verifyFailed')}</Badge>
    }
    return <Badge variant="outline">{t('system.settings.archive.verifyPending')}</Badge>
  }

  return (
    <Sheet open={jobId !== null} onOpenChange={onOpenChange}>
      <SheetContent className="w-full gap-0 overflow-y-auto sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle>{t('system.settings.archive.detailTitle')}</SheetTitle>
          <SheetDescription>
            {jobId !== null && `#${String(jobId)}`}
            {detail && ` · ${t(`system.settings.archive.status${statusSuffix(detail.status)}`)}`}
          </SheetDescription>
        </SheetHeader>
        <div className="px-4 pb-6">
          <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
            {detail && (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t('system.settings.archive.itemDomain')}</TableHead>
                    <TableHead>{t('system.settings.archive.itemPhase')}</TableHead>
                    <TableHead>{t('system.settings.archive.itemProgress')}</TableHead>
                    <TableHead>{t('system.settings.archive.itemDeleted')}</TableHead>
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
                      <TableCell className="tabular-nums">{formatCount(item.rowsDeleted)}</TableCell>
                      <TableCell>{verifyBadge(item)}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </AsyncSection>
        </div>
      </SheetContent>
    </Sheet>
  )
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
