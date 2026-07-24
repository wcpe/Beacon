// 文件清单（主从布局）：ListCard 吸顶筛选（子服 / 路径前缀 / 文件名 / 扩展名）+ 自区滚列表 + 吸底分页，
// 勾选子服批量触发重扫（离线服本批跳过）；点行打开右侧非模态详情面板（元数据 / 预览 / 哈希）。
import { useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  AsyncSection,
  Badge,
  Button,
  Checkbox,
  DataTable,
  Input,
  TableSkeleton,
  type DataTableColumn,
} from '@beacon/ui'
import type { AssetItem, AssetRescanResponse } from '@beacon/contracts'

import MasterDetail from '../../features/shared/master-detail'
import Pager from '../../features/delivery/pager'
import ListCard from '../../features/shared/list-card'
import { ApiClientError } from '../../api/delivery'
import { fetchAssets, rescanAssets } from '../../api/delivery-assets'
import ManifestDetailPanel from './manifest-detail-panel'
import RescanDialog from './rescan-dialog'
import { formatBytes, formatTime, shortHash } from './format'

const PAGE_SIZE = 15

export default function ManifestPanel({ namespaceId }: { namespaceId: number }) {
  const { t } = useTranslation()

  const [serverId, setServerId] = useState('')
  const [pathPrefix, setPathPrefix] = useState('')
  const [name, setName] = useState('')
  const [ext, setExt] = useState('')
  const [page, setPage] = useState(1)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [detail, setDetail] = useState<AssetItem | null>(null)
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

  const rowKeyOf = (row: AssetItem): string => `${row.serverId}:${row.path}`

  const columns = useMemo<DataTableColumn<AssetItem>[]>(
    () => [
      {
        header: '',
        headClassName: 'w-8',
        cell: (row) => (
          <span
            className="inline-flex"
            onClick={(e) => {
              // 勾选用于批量重扫，阻止冒泡到行点击（行点击用于打开详情面板）
              e.stopPropagation()
            }}
          >
            <Checkbox
              checked={selected.has(row.serverId)}
              onCheckedChange={() => {
                toggleRow(row.serverId)
              }}
              aria-label={t('common.selectRow', { id: row.serverId })}
            />
          </span>
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
        cell: (row) => <span className="tnum text-xs text-ink-3">{formatTime(new Date(row.mtimeMs).toISOString())}</span>,
      },
      {
        header: t('delivery.assets.list.columns.type'),
        cell: (row) =>
          row.isText ? (
            <Badge variant="brand">{t('delivery.assets.list.text')}</Badge>
          ) : (
            <Badge variant="off" className="gap-1.5">
              <span className="size-1.5 rounded-full bg-current" />
              {t('delivery.assets.list.binary')}
            </Badge>
          ),
      },
    ],
    [t, selected],
  )

  const setFilter = (setter: (v: string) => void, value: string): void => {
    setter(value)
    setPage(1)
  }

  const toolbar = (
    <div className="grid gap-2.5">
      {/* 筛选条 */}
      <div className="flex flex-wrap items-center gap-2">
        <Input
          aria-label={t('delivery.assets.list.filters.serverId')}
          placeholder={t('delivery.assets.list.filters.serverId')}
          value={serverId}
          onChange={(e) => {
            setFilter(setServerId, e.target.value)
          }}
          className="w-36"
        />
        <Input
          aria-label={t('delivery.assets.list.filters.pathPrefix')}
          placeholder={t('delivery.assets.list.filters.pathPrefix')}
          value={pathPrefix}
          onChange={(e) => {
            setFilter(setPathPrefix, e.target.value)
          }}
          className="w-44"
        />
        <Input
          aria-label={t('delivery.assets.list.filters.name')}
          placeholder={t('delivery.assets.list.filters.name')}
          value={name}
          onChange={(e) => {
            setFilter(setName, e.target.value)
          }}
          className="w-36"
        />
        <Input
          aria-label={t('delivery.assets.list.filters.ext')}
          placeholder={t('delivery.assets.list.filters.ext')}
          value={ext}
          onChange={(e) => {
            setFilter(setExt, e.target.value)
          }}
          className="w-24"
        />
      </div>

      {nameNeedsIndex && (
        <p className="rounded-lg border border-warn-bd bg-warn-bg px-3 py-2 text-sm text-warn">
          {t('delivery.assets.list.needIndex')}
        </p>
      )}

      {/* 批量选择集操作条 */}
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
    </div>
  )

  const master = (
    <ListCard
      toolbar={toolbar}
      footer={
        !nameNeedsIndex && total > PAGE_SIZE ? (
          <Pager page={page} total={total} pageSize={PAGE_SIZE} onPageChange={setPage} />
        ) : undefined
      }
    >
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
          onRowClick={(row) => {
            setDetail(row)
          }}
          rowClassName={(row) => (detail && rowKeyOf(row) === rowKeyOf(detail) ? 'bg-brand-50/60' : undefined)}
        />
      </AsyncSection>
    </ListCard>
  )

  return (
    <>
      <MasterDetail
        master={master}
        detail={detail ? <ManifestDetailPanel item={detail} /> : null}
        detailTitle={detail ? <span className="font-mono">{detail.path}</span> : ''}
        closeLabel={t('delivery.assets.preview.close')}
        onClose={() => {
          setDetail(null)
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
    </>
  )
}
