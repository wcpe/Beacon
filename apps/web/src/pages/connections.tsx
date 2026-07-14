// 连接明细页（/connections，FR-181）：连接会话明细查询与追溯。
// 查询防护（spec §4.3）：精确 connId 直查；否则必须 serverId / 玩家 UUID + 时间范围（≤168h），
// 未满足条件时不发请求、展示引导空态。游标分页（热 / 冷原生 CursorPage）；「包含归档」冷查询（FR-152）。
// 行点击右侧非模态详情面板（行数据自足，无需二次请求）。
import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Cable, Search } from 'lucide-react'

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
import type { ConnectionItem } from '@beacon/contracts'

import { fetchConnections, type ConnectionsQuery } from '../api/connections'
import FilterSelect from '../features/observability/filter-select'
import QueryField from '../features/observability/query-field'
import CursorPager from '../features/observability/cursor-pager'
import { useCursorStack } from '../features/observability/use-cursor-stack'
import WindowSelect, { WINDOW_MS, type WindowKey } from '../features/observability/window-select'
import ListCard from '../features/shared/list-card'
import MasterDetail from '../features/shared/master-detail'

const PAGE_SIZE = 20
const STATUSES = ['open', 'closed'] as const
const CLOSE_KINDS = ['quit', 'kick', 'timeout', 'proxy_shutdown', 'error'] as const

// 已提交的查询条件（点「查询」才提交，避免防护条件半填时打请求）
interface Committed {
  connId?: string
  serverId?: string
  playerUuid?: string
  status?: string
  closeKind?: string
  windowKey: WindowKey
  cold: boolean
}

export default function ConnectionsPage() {
  const { t } = useTranslation()
  const [connId, setConnId] = useState('')
  const [serverId, setServerId] = useState('')
  const [playerUuid, setPlayerUuid] = useState('')
  const [status, setStatus] = useState('all')
  const [closeKind, setCloseKind] = useState('all')
  const [windowKey, setWindowKey] = useState<WindowKey>('1h')
  const [cold, setCold] = useState(false)
  const [committed, setCommitted] = useState<Committed | null>(null)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const cursor = useCursorStack()

  // 查询防护前置判定：精确 connId 或至少一个选择性过滤（serverId / playerUuid）
  const exactMode = connId.trim() !== ''
  const canSearch = exactMode || serverId.trim() !== '' || playerUuid.trim() !== ''

  const submit = () => {
    cursor.reset()
    setSelectedId(null)
    setCommitted({
      connId: connId.trim() || undefined,
      serverId: serverId.trim() || undefined,
      playerUuid: playerUuid.trim() || undefined,
      status: status === 'all' ? undefined : status,
      closeKind: closeKind === 'all' ? undefined : closeKind,
      windowKey,
      cold,
    })
  }

  const query = useQuery({
    queryKey: ['connections', 'list', committed, cursor.cursor],
    enabled: committed !== null,
    queryFn: () => {
      // enabled 已保证非空；此守卫仅为类型收窄（不可达）
      if (committed === null) {
        return Promise.reject(new Error('查询条件未提交'))
      }
      const c = committed
      const q: ConnectionsQuery = {
        connId: c.connId,
        serverId: c.serverId,
        playerUuid: c.playerUuid,
        status: c.status,
        closeKind: c.closeKind,
        cursor: cursor.cursor === '' ? undefined : cursor.cursor,
        limit: PAGE_SIZE,
        includeArchived: c.cold ? true : undefined,
      }
      // 精确 connId 直查免时间范围；条件查询按预设窗口自「现在」往前推
      if (c.connId === undefined) {
        const to = Date.now()
        q.from = new Date(to - WINDOW_MS[c.windowKey]).toISOString()
        q.to = new Date(to).toISOString()
      }
      return fetchConnections(q)
    },
    placeholderData: keepPreviousData,
  })

  const rows = query.data?.items ?? []
  const nextCursor = query.data?.nextCursor ?? null
  const selected = rows.find((r) => r.connId === selectedId) ?? null
  const dash = t('observability.connections.dash')

  // 详情面板打开时收起次要列（后端路径 / 时长 / 断开类别在详情内均可见），避免主表被挤出横向滚动
  const detailOpen = selected !== null
  const columns = useMemo<DataTableColumn<ConnectionItem>[]>(() => {
    const all: (DataTableColumn<ConnectionItem> & { secondary?: boolean })[] = [
      {
        header: t('observability.connections.columns.openedAt'),
        cell: (row) => (
          <span className="tabular-nums text-xs text-ink-3">{new Date(row.openedAt).toLocaleString()}</span>
        ),
      },
      {
        header: t('observability.connections.columns.player'),
        cell: (row) => <span className="font-mono text-xs text-ink-1">{row.playerName}</span>,
      },
      {
        header: t('observability.connections.columns.proxy'),
        cell: (row) => <span className="font-mono text-xs text-ink-2">{row.proxyServerId}</span>,
      },
      {
        header: t('observability.connections.columns.backendPath'),
        secondary: true,
        cell: (row) => (
          <span className="font-mono text-xs text-ink-2">
            {row.firstBackendServerId ?? dash}
            {row.backendSwitchCount > 0 && ` → ${row.lastBackendServerId ?? dash}`}
          </span>
        ),
      },
      {
        header: t('observability.connections.columns.duration'),
        secondary: true,
        cell: (row) => (
          <span className="tabular-nums text-xs text-ink-3">
            {row.durationMs === null
              ? dash
              : t('observability.connections.durationSec', { count: Math.round(row.durationMs / 1000) })}
          </span>
        ),
      },
      {
        header: t('observability.connections.columns.status'),
        cell: (row) => (
          <Badge variant={row.status === 'open' ? 'ok' : 'off'}>
            {t(`observability.connections.status.${row.status}`)}
          </Badge>
        ),
      },
      {
        header: t('observability.connections.columns.closeKind'),
        secondary: true,
        cell: (row) =>
          row.closeKind === null ? (
            <span className="text-xs text-ink-4">{dash}</span>
          ) : (
            <Badge variant={row.closeKind === 'error' || row.closeKind === 'timeout' ? 'crit' : 'off'}>
              {t(`observability.connections.closeKind.${row.closeKind}`)}
            </Badge>
          ),
      },
    ]
    return detailOpen ? all.filter((col) => col.secondary !== true) : all
  }, [t, dash, detailOpen])

  const toolbar = (
    <div className="grid gap-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <span className="mr-1 flex items-center gap-2 text-[13px] font-semibold text-ink-1">
          <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
            <Cable className="size-[15px]" />
          </span>
          {t('observability.connections.listTitle')}
        </span>
        <span className="text-xs text-ink-4">{t('observability.connections.guardHint')}</span>
      </div>
      <div className="flex flex-wrap items-end gap-2">
        <QueryField label={t('observability.connections.filterConnId')}>
          <Input
            aria-label={t('observability.connections.filterConnId')}
            placeholder={t('observability.connections.filterConnId')}
            value={connId}
            onChange={(e) => {
              setConnId(e.target.value)
            }}
            className="w-60 font-mono"
          />
        </QueryField>
        <QueryField label={t('observability.connections.filterServer')}>
          <Input
            aria-label={t('observability.connections.filterServer')}
            placeholder={t('observability.connections.filterServer')}
            value={serverId}
            onChange={(e) => {
              setServerId(e.target.value)
            }}
            className="w-44"
            disabled={exactMode}
          />
        </QueryField>
        <QueryField label={t('observability.connections.filterPlayer')}>
          <Input
            aria-label={t('observability.connections.filterPlayer')}
            placeholder={t('observability.connections.filterPlayer')}
            value={playerUuid}
            onChange={(e) => {
              setPlayerUuid(e.target.value)
            }}
            className="w-60 font-mono"
            disabled={exactMode}
          />
        </QueryField>
        <QueryField label={t('observability.connections.filterStatus')}>
          <FilterSelect
            label={t('observability.connections.filterStatus')}
            value={status}
            options={STATUSES.map((v) => ({ value: v, label: t(`observability.connections.status.${v}`) }))}
            onChange={setStatus}
          />
        </QueryField>
        <QueryField label={t('observability.connections.filterCloseKind')}>
          <FilterSelect
            label={t('observability.connections.filterCloseKind')}
            value={closeKind}
            options={CLOSE_KINDS.map((v) => ({ value: v, label: t(`observability.connections.closeKind.${v}`) }))}
            onChange={setCloseKind}
          />
        </QueryField>
        <QueryField label={t('observability.connections.filterWindow')}>
          <WindowSelect value={windowKey} keys={['1h', '6h', '24h', '7d']} onChange={setWindowKey} />
        </QueryField>
        <label
          className="flex h-9 cursor-pointer items-center gap-2 text-sm text-ink-2"
          title={t('observability.common.includeArchivedHint')}
        >
          <Checkbox
            checked={cold}
            onCheckedChange={(v) => {
              setCold(v === true)
            }}
            aria-label={t('observability.common.includeArchived')}
          />
          {t('observability.common.includeArchived')}
        </label>
        {/* 禁用态用 outline 变体拉开与可点态的视觉差（灰边框空心 vs 品牌实心） */}
        <Button size="sm" variant={canSearch ? 'default' : 'outline'} disabled={!canSearch} onClick={submit}>
          <Search className="size-3.5" />
          {t('observability.connections.search')}
        </Button>
      </div>
    </div>
  )

  return (
    <section className="grid gap-5">
      <SectionHeader
        size="lg"
        icon={<Cable className="size-5" />}
        title={t('nav.connections')}
        count={t('observability.connections.mission')}
      />
      <MasterDetail
        master={
          <ListCard
            toolbar={toolbar}
            footer={
              committed !== null && (nextCursor !== null || cursor.canPrev) ? (
                <CursorPager
                  pageIndex={cursor.pageIndex}
                  canPrev={cursor.canPrev}
                  canNext={nextCursor !== null}
                  onPrev={cursor.goPrev}
                  onNext={() => {
                    if (nextCursor !== null) {
                      cursor.goNext(nextCursor)
                    }
                  }}
                  cold={committed.cold}
                />
              ) : undefined
            }
          >
            {committed === null ? (
              <p className="rounded-xl border border-dashed border-border bg-card/60 px-4 py-10 text-center text-sm text-ink-3">
                {t('observability.connections.guardEmpty')}
              </p>
            ) : (
              <AsyncSection
                isLoading={query.isLoading}
                isError={query.isError}
                error={query.error}
                skeleton={<TableSkeleton columns={columns.length} rows={8} />}
              >
                <DataTable
                  columns={columns}
                  rows={rows}
                  rowKey={(row) => row.connId}
                  emptyText={t('observability.connections.listEmpty')}
                  density="compact"
                  onRowClick={(row) => {
                    setSelectedId(row.connId)
                  }}
                  rowClassName={(row) => (row.connId === selectedId ? 'bg-brand-50/60' : undefined)}
                />
              </AsyncSection>
            )}
          </ListCard>
        }
        detail={selected === null ? null : <ConnDetailPanel row={selected} />}
        detailTitle={t('observability.connections.detailTitle')}
        closeLabel={t('observability.common.close')}
        onClose={() => {
          setSelectedId(null)
        }}
      />
    </section>
  )
}

// 连接详情面板：行数据自足直显（无需二次请求）。
function ConnDetailPanel({ row }: { row: ConnectionItem }) {
  const { t } = useTranslation()
  const dash = t('observability.connections.dash')
  const fields: [string, string][] = [
    ['connId', row.connId],
    ['player', row.playerName],
    ['playerUuid', row.playerUuid],
    ['clientIp', row.clientIp],
    ['protocolVersion', String(row.protocolVersion)],
    ['proxy', row.proxyServerId],
    ['firstBackend', row.firstBackendServerId ?? dash],
    ['lastBackend', row.lastBackendServerId ?? dash],
    ['backendSwitches', String(row.backendSwitchCount)],
    ['openedAt', new Date(row.openedAt).toLocaleString()],
    ['closedAt', row.closedAt === null ? dash : new Date(row.closedAt).toLocaleString()],
    [
      'duration',
      row.durationMs === null
        ? dash
        : t('observability.connections.durationSec', { count: Math.round(row.durationMs / 1000) }),
    ],
    ['closeKind', row.closeKind === null ? dash : t(`observability.connections.closeKind.${row.closeKind}`)],
    ['closeReason', row.closeReason ?? dash],
  ]
  return (
    <div className="grid gap-2.5 text-sm">
      <div className="flex items-center gap-2">
        <Badge variant={row.status === 'open' ? 'ok' : 'off'}>
          {t(`observability.connections.status.${row.status}`)}
        </Badge>
        <span className="truncate font-mono text-xs text-ink-3">{row.connId}</span>
      </div>
      <dl className="grid gap-1.5">
        {fields.map(([key, value]) => (
          <div key={key} className="flex items-baseline justify-between gap-3">
            <dt className="shrink-0 text-xs text-ink-4">{t(`observability.connections.fields.${key}`)}</dt>
            <dd className="truncate text-right font-mono text-xs text-ink-1" title={value}>
              {value}
            </dd>
          </div>
        ))}
      </dl>
    </div>
  )
}
