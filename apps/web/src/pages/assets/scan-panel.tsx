// 扫描概要面板：每服清单摘要（文件数 / 总大小 / 清单摘要 / 扫描时间 / 耗时），服务端分页。
import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ScanLine } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  DataTable,
  SectionHeader,
  SummaryStrip,
  TableSkeleton,
  type DataTableColumn,
  type SummaryItem,
} from '@beacon/ui'
import type { AssetScanStatusItem } from '@beacon/devmock'

import Pager from '../../features/delivery/pager'
import { fetchScanStatus } from '../../api/delivery-assets'
import { formatBytes, formatTime, shortHash } from './format'

const PAGE_SIZE = 10

export default function ScanPanel({ namespaceId }: { namespaceId: number }) {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)

  const query = useQuery({
    queryKey: ['assets', 'scan-status', namespaceId, page],
    queryFn: () => fetchScanStatus(namespaceId, undefined, page, PAGE_SIZE),
    placeholderData: keepPreviousData,
    enabled: namespaceId > 0,
  })

  const total = query.data?.total ?? 0
  const items = query.data?.items ?? []

  // 概要小卡：从当前页已取到的清单派生本页合计（本页子服数 / 文件数 / 总大小），不额外取数
  const summary = useMemo<SummaryItem[]>(() => {
    if (items.length === 0) return []
    const fileCount = items.reduce((sum, it) => sum + it.fileCount, 0)
    const totalSize = items.reduce((sum, it) => sum + it.totalSize, 0)
    return [
      { label: t('delivery.assets.scan.summary.servers'), value: items.length },
      { label: t('delivery.assets.scan.summary.files'), value: fileCount },
      { label: t('delivery.assets.scan.summary.size'), value: formatBytes(totalSize) },
    ]
  }, [items, t])

  const columns = useMemo<DataTableColumn<AssetScanStatusItem>[]>(
    () => [
      {
        header: t('delivery.assets.scan.columns.serverId'),
        cell: (row) => <span className="font-mono">{row.serverId}</span>,
      },
      { header: t('delivery.assets.scan.columns.fileCount'), cell: (row) => row.fileCount },
      {
        header: t('delivery.assets.scan.columns.totalSize'),
        cell: (row) => formatBytes(row.totalSize),
      },
      {
        header: t('delivery.assets.scan.columns.digest'),
        cell: (row) => (
          <span className="flex items-center gap-1.5">
            <span className="font-mono text-xs text-ink-3">
              {shortHash(row.manifestDigest)}
            </span>
            {row.truncated && (
              <Badge variant="warn" className="gap-1.5">
                <span className="size-1.5 rounded-full bg-current" />
                {t('delivery.assets.scan.truncated')}
              </Badge>
            )}
          </span>
        ),
      },
      {
        header: t('delivery.assets.scan.columns.scannedAt'),
        cell: (row) => formatTime(row.scannedAt),
      },
      {
        header: t('delivery.assets.scan.columns.duration'),
        cell: (row) => `${String(row.scanDurationMs)} ms`,
      },
    ],
    [t],
  )

  return (
    <section className="grid gap-3">
      <SectionHeader
        icon={<ScanLine className="size-4" aria-hidden />}
        title={t('delivery.assets.scan.title')}
      />
      {summary.length > 0 && <SummaryStrip items={summary} />}
      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<TableSkeleton columns={6} rows={4} />}
      >
        <DataTable
          columns={columns}
          rows={query.data?.items}
          rowKey={(row) => row.serverId}
          emptyText={t('delivery.assets.scan.empty')}
          density="compact"
        />
      </AsyncSection>
      <Pager page={page} total={total} pageSize={PAGE_SIZE} onPageChange={setPage} />
    </section>
  )
}
