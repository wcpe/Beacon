// 扫描概要面板：每服清单摘要（文件数 / 总大小 / 清单摘要 / 扫描时间 / 耗时），服务端分页。
import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  AsyncSection,
  Badge,
  DataTable,
  SectionHeader,
  TableSkeleton,
  type DataTableColumn,
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
            <span className="font-mono text-xs text-muted-foreground">
              {shortHash(row.manifestDigest)}
            </span>
            {row.truncated && <Badge variant="outline">{t('delivery.assets.scan.truncated')}</Badge>}
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
      <SectionHeader title={t('delivery.assets.scan.title')} />
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
