// 扫描概要面板：每服清单摘要（文件数 / 总大小 / 清单摘要 / 扫描时间 / 耗时），服务端分页。
import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { CircleAlert, Files, HardDrive, RefreshCw, ScanLine, Server } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Button,
  CardDescription,
  CardTitle,
  CardGridSkeleton,
  DataTable,
  KpiCard,
  SectionHeader,
  TableSkeleton,
  type DataTableColumn,
} from '@beacon/ui'

import ListCard from '../../features/shared/list-card'
import type { AssetScanStatusItem } from '@beacon/contracts'

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

  // 概要指标只从当前页已取到的清单派生，不额外取数。
  const summary = useMemo(() => {
    if (items.length === 0) return null
    return {
      serverCount: items.length,
      fileCount: items.reduce((sum, item) => sum + item.fileCount, 0),
      totalSize: formatBytes(items.reduce((sum, item) => sum + item.totalSize, 0)),
    }
  }, [items])

  const columns = useMemo<DataTableColumn<AssetScanStatusItem>[]>(
    () => [
      {
        header: t('delivery.assets.scan.columns.serverId'),
        cell: (row) => <span className="font-mono" title={row.serverId}>{row.serverId}</span>,
      },
      {
        header: t('delivery.assets.scan.columns.fileCount'),
        headClassName: 'text-right',
        className: 'text-right tabular-nums',
        cell: (row) => row.fileCount,
      },
      {
        header: t('delivery.assets.scan.columns.totalSize'),
        headClassName: 'text-right',
        className: 'text-right tabular-nums',
        cell: (row) => formatBytes(row.totalSize),
      },
      {
        header: t('delivery.assets.scan.columns.digest'),
        cell: (row) => (
          <span className="flex items-center gap-1.5">
            <span className="font-mono text-xs text-ink-3" title={row.manifestDigest}>
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
        headClassName: 'text-right',
        className: 'text-right tabular-nums',
        cell: (row) => `${String(row.scanDurationMs)} ms`,
      },
    ],
    [t],
  )

  return (
    <section className="grid gap-3.5 md:min-h-0 md:flex-1 md:flex md:flex-col" aria-label={t('delivery.assets.scan.title')}>
      <SectionHeader
        icon={<ScanLine className="size-4" aria-hidden />}
        title={t('delivery.assets.scan.title')}
        size="lg"
      />
      {query.isLoading ? (
        <CardGridSkeleton count={3} gridClass="grid grid-cols-1 gap-3 sm:grid-cols-3" />
      ) : summary !== null ? (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <KpiCard
            icon={<Server className="size-4" />}
            label={t('delivery.assets.scan.summary.servers')}
            value={summary.serverCount}
            tone="brand"
          />
          <KpiCard
            icon={<Files className="size-4" />}
            label={t('delivery.assets.scan.summary.files')}
            value={summary.fileCount}
            tone="ok"
          />
          <KpiCard
            icon={<HardDrive className="size-4" />}
            label={t('delivery.assets.scan.summary.size')}
            value={summary.totalSize}
            tone="off"
          />
        </div>
      ) : null}

      <ListCard
        fill
        toolbar={
          <div className="grid gap-2">
            <SectionHeader
              icon={<Files className="size-4" aria-hidden />}
              title={t('delivery.assets.scan.tableTitle')}
              count={t('delivery.assets.scan.tableCount', { total })}
              className="border-0 pb-0"
            />
            <CardDescription>{t('delivery.tips.assetsScanHint')}</CardDescription>
          </div>
        }
        footer={total > PAGE_SIZE ? <Pager page={page} total={total} pageSize={PAGE_SIZE} onPageChange={setPage} /> : undefined}
      >
        {query.isError ? (
          <div className="flex flex-wrap items-center gap-3 py-3" role="alert">
            <CircleAlert className="size-4 shrink-0 text-destructive" aria-hidden />
            <CardDescription className="flex-1">{t('delivery.assets.scan.loadFailed')}</CardDescription>
            <Button size="sm" variant="outline" onClick={() => { void query.refetch() }}>
              <RefreshCw className="size-3.5" aria-hidden />
              {t('delivery.assets.scan.retry')}
            </Button>
          </div>
        ) : (
          <AsyncSection isLoading={query.isLoading} skeleton={<TableSkeleton columns={6} rows={4} />}>
            {items.length === 0 ? (
              <div className="grid justify-items-center gap-3 py-8 text-center">
                <ScanLine className="size-8 text-ink-4" aria-hidden />
                <div className="grid justify-items-center gap-1">
                  <CardTitle>{t('delivery.assets.scan.emptyTitle')}</CardTitle>
                  <CardDescription className="max-w-md text-center">
                    {t('delivery.assets.scan.emptyDescription')}
                  </CardDescription>
                </div>
                <Button size="sm" variant="outline" onClick={() => { void query.refetch() }}>
                  <RefreshCw className="size-3.5" aria-hidden />
                  {t('delivery.assets.scan.refresh')}
                </Button>
              </div>
            ) : (
              <DataTable
                columns={columns}
                rows={items}
                rowKey={(row) => row.serverId}
                emptyText={t('delivery.assets.scan.empty')}
                density="compact"
                stickyHeader
              />
            )}
          </AsyncSection>
        )}
      </ListCard>
    </section>
  )
}
