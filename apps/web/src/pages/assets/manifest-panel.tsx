// 文件清单面板：按子服 / 路径前缀 / 文件名 / 扩展名 / 哈希搜索分页；行内预览；
// 勾选子服批量触发重扫（离线服本批跳过）。
import { useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { FileText } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Button,
  Checkbox,
  DataTable,
  Input,
  SectionHeader,
  TableSkeleton,
  type DataTableColumn,
} from '@beacon/ui'
import type { AssetItem, AssetRescanResponse } from '@beacon/devmock'

import Pager from '../../features/delivery/pager'
import { ApiClientError } from '../../api/delivery'
import { fetchAssets, rescanAssets } from '../../api/delivery-assets'
import PreviewDialog from './preview-dialog'
import RescanDialog from './rescan-dialog'
import { formatBytes, formatTime, shortHash } from './format'

const PAGE_SIZE = 15

interface PreviewTarget {
  serverId: string
  path: string
}

export default function ManifestPanel({ namespaceId }: { namespaceId: number }) {
  const { t } = useTranslation()

  const [serverId, setServerId] = useState('')
  const [pathPrefix, setPathPrefix] = useState('')
  const [name, setName] = useState('')
  const [ext, setExt] = useState('')
  const [page, setPage] = useState(1)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [preview, setPreview] = useState<PreviewTarget | null>(null)
  const [rescanOpen, setRescanOpen] = useState(false)
  const [rescanResult, setRescanResult] = useState<AssetRescanResponse | null>(null)
  const [rescanError, setRescanError] = useState<string | null>(null)

  const trimmed = {
    serverId: serverId.trim() === '' ? undefined : serverId.trim(),
    pathPrefix: pathPrefix.trim() === '' ? undefined : pathPrefix.trim(),
    name: name.trim() === '' ? undefined : name.trim(),
    ext: ext.trim() === '' ? undefined : ext.trim(),
  }
  // 后端约束：按文件名搜索需同时带一个索引条件（serverId / pathPrefix / ext / sha256）
  const nameNeedsIndex =
    trimmed.name !== undefined &&
    trimmed.serverId === undefined &&
    trimmed.pathPrefix === undefined &&
    trimmed.ext === undefined

  const query = useQuery({
    queryKey: ['assets', 'manifest', namespaceId, trimmed, page],
    queryFn: () => fetchAssets({ namespaceId, ...trimmed, page, pageSize: PAGE_SIZE }),
    placeholderData: keepPreviousData,
    enabled: namespaceId > 0 && !nameNeedsIndex,
  })

  const total = query.data?.total ?? 0

  const rescanMutation = useMutation({
    mutationFn: (serverIds: string[]) => rescanAssets({ namespaceId, serverIds }),
    onSuccess: (data) => {
      setRescanResult(data)
      setRescanError(null)
    },
    onError: (error) => {
      setRescanError(error instanceof ApiClientError ? error.message : String(error))
    },
  })

  const toggleRow = (id: string): void => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) {
        next.delete(id)
      } else {
        next.add(id)
      }
      return next
    })
  }

  const columns = useMemo<DataTableColumn<AssetItem>[]>(
    () => [
      {
        header: '',
        headClassName: 'w-8',
        cell: (row) => (
          <Checkbox
            checked={selected.has(row.serverId)}
            onCheckedChange={() => {
              toggleRow(row.serverId)
            }}
            aria-label={`选择 ${row.serverId}`}
          />
        ),
      },
      {
        header: t('delivery.assets.list.columns.serverId'),
        cell: (row) => <span className="font-mono">{row.serverId}</span>,
      },
      {
        header: t('delivery.assets.list.columns.path'),
        cell: (row) => <span className="font-mono text-xs text-ink-2">{row.path}</span>,
      },
      { header: t('delivery.assets.list.columns.ext'), cell: (row) => row.ext || '-' },
      { header: t('delivery.assets.list.columns.size'), cell: (row) => formatBytes(row.size) },
      {
        header: t('delivery.assets.list.columns.sha256'),
        cell: (row) => <span className="font-mono text-xs text-ink-3">{shortHash(row.sha256)}</span>,
      },
      {
        header: t('delivery.assets.list.columns.mtime'),
        cell: (row) => formatTime(new Date(row.mtimeMs).toISOString()),
      },
      {
        header: t('delivery.assets.list.columns.actions'),
        cell: (row) => (
          <div className="flex items-center gap-1.5">
            {row.isText ? (
              <Badge variant="brand">{t('delivery.assets.list.text')}</Badge>
            ) : (
              <Badge variant="off" className="gap-1.5">
                <span className="size-1.5 rounded-full bg-current" />
                {t('delivery.assets.list.binary')}
              </Badge>
            )}
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                setPreview({ serverId: row.serverId, path: row.path })
              }}
            >
              {t('delivery.assets.list.preview')}
            </Button>
          </div>
        ),
      },
    ],
    [t, selected],
  )

  const setFilter = (setter: (v: string) => void, value: string): void => {
    setter(value)
    setPage(1)
  }

  return (
    <section className="grid gap-3">
      <SectionHeader
        icon={<FileText className="size-4" aria-hidden />}
        title={t('delivery.assets.list.title')}
      />

      {/* 筛选条 */}
      <div className="flex flex-wrap items-center gap-2">
        <Input
          aria-label={t('delivery.assets.list.filters.serverId')}
          placeholder={t('delivery.assets.list.filters.serverId')}
          value={serverId}
          onChange={(e) => {
            setFilter(setServerId, e.target.value)
          }}
          className="w-40"
        />
        <Input
          aria-label={t('delivery.assets.list.filters.pathPrefix')}
          placeholder={t('delivery.assets.list.filters.pathPrefix')}
          value={pathPrefix}
          onChange={(e) => {
            setFilter(setPathPrefix, e.target.value)
          }}
          className="w-48"
        />
        <Input
          aria-label={t('delivery.assets.list.filters.name')}
          placeholder={t('delivery.assets.list.filters.name')}
          value={name}
          onChange={(e) => {
            setFilter(setName, e.target.value)
          }}
          className="w-40"
        />
        <Input
          aria-label={t('delivery.assets.list.filters.ext')}
          placeholder={t('delivery.assets.list.filters.ext')}
          value={ext}
          onChange={(e) => {
            setFilter(setExt, e.target.value)
          }}
          className="w-28"
        />
      </div>

      {nameNeedsIndex && (
        <p className="rounded-lg border border-warn-bd bg-warn-bg px-3 py-2 text-sm text-warn">
          {t('delivery.assets.list.needIndex')}
        </p>
      )}

      {/* 批量选择集顶部操作条 */}
      {selected.size > 0 && (
        <div className="flex items-center gap-3 rounded-lg border border-brand-100 bg-brand-50 px-3 py-2 text-sm text-ink-2">
          <span>{t('delivery.assets.rescan.selected', { count: selected.size })}</span>
          <Button
            size="sm"
            onClick={() => {
              setRescanResult(null)
              setRescanError(null)
              setRescanOpen(true)
            }}
          >
            {t('delivery.assets.rescan.action')}
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              setSelected(new Set())
            }}
          >
            {t('delivery.assets.rescan.pickHint')}
          </Button>
        </div>
      )}

      <AsyncSection
        isLoading={query.isLoading && !nameNeedsIndex}
        isError={query.isError}
        error={query.error}
        skeleton={<TableSkeleton columns={8} rows={6} />}
      >
        <DataTable
          columns={columns}
          rows={nameNeedsIndex ? [] : query.data?.items}
          rowKey={(row, index) => `${row.serverId}:${row.path}:${String(index)}`}
          emptyText={t('delivery.assets.list.empty')}
          density="compact"
        />
      </AsyncSection>

      <Pager page={page} total={total} pageSize={PAGE_SIZE} onPageChange={setPage} />

      <PreviewDialog
        target={preview}
        onOpenChange={(open) => {
          if (!open) {
            setPreview(null)
          }
        }}
      />

      <RescanDialog
        open={rescanOpen}
        serverIds={[...selected]}
        pending={rescanMutation.isPending}
        result={rescanResult}
        errorText={rescanError}
        onConfirm={() => {
          rescanMutation.mutate([...selected])
        }}
        onOpenChange={(open) => {
          setRescanOpen(open)
          if (!open) {
            setRescanResult(null)
            setRescanError(null)
          }
        }}
      />
    </section>
  )
}
