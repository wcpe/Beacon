// 消息链路页（/messages，FR-181）：跨服消息链路检索与逐跳追踪。元数据永不含 payload；
// payload 仅经受控查看弹窗（原因必填 + 先审计后返回）按需获取。
// 查询防护：精确 messageId / correlationId 直查；热查询可仅时间窗（全局近期）；冷查询仍须 selector。
// 进页默认 committed 近 1h 全局查询。游标分页；「包含归档」冷查询（FR-152）。行点击右侧详情面板。
import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { MessagesSquare, Search } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Button,
  Checkbox,
  DataTable,
  Input,
  PageHeader,
  Skeleton,
  TableSkeleton,
  type DataTableColumn,
} from '@beacon/ui'
import type { MessageItem, MsgStatus } from '@beacon/contracts'

import { fetchMessageDetail, fetchMessages, type MessagesQuery } from '../api/connections'
import FilterSelect from '../features/observability/filter-select'
import QueryField from '../features/observability/query-field'
import CursorPager from '../features/observability/cursor-pager'
import { useCursorStack } from '../features/observability/use-cursor-stack'
import WindowSelect, { WINDOW_MS, type WindowKey } from '../features/observability/window-select'
import PayloadDialog from '../features/observability/payload-dialog'
import ListCard from '../features/shared/list-card'
import MasterDetail from '../features/shared/master-detail'

const PAGE_SIZE = 20
const STATUSES = ['accepted', 'dispatched', 'delivered', 'failed', 'expired'] as const
const TARGET_KINDS = ['server', 'player', 'broadcast'] as const

// 已提交的查询条件（点「查询」才提交，避免防护条件半填时打请求）
interface Committed {
  messageId?: string
  correlationId?: string
  serverId?: string
  playerUuid?: string
  status?: string
  targetKind?: string
  windowKey: WindowKey
  cold: boolean
}

// 消息状态 → 语义药丸变体
function statusTone(status: MsgStatus): 'ok' | 'crit' | 'warn' | 'off' {
  if (status === 'delivered') {
    return 'ok'
  }
  if (status === 'failed') {
    return 'crit'
  }
  if (status === 'expired') {
    return 'warn'
  }
  return 'off'
}

export default function MessagesPage() {
  const { t } = useTranslation()
  const [messageId, setMessageId] = useState('')
  const [correlationId, setCorrelationId] = useState('')
  const [serverId, setServerId] = useState('')
  const [playerUuid, setPlayerUuid] = useState('')
  const [status, setStatus] = useState('all')
  const [targetKind, setTargetKind] = useState('all')
  const [windowKey, setWindowKey] = useState<WindowKey>('1h')
  const [cold, setCold] = useState(false)
  // 进页默认近 1h 全局热查询（无需 selector）
  const [committed, setCommitted] = useState<Committed>(() => ({ windowKey: '1h', cold: false }))
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const cursor = useCursorStack()

  // 查询防护：精确 ID 始终可查；热查询有时间窗即可；冷查询仍须 serverId / playerUuid
  const exactMode = messageId.trim() !== '' || correlationId.trim() !== ''
  const hasSelector = serverId.trim() !== '' || playerUuid.trim() !== ''
  const canSearch = exactMode || !cold || hasSelector

  const submit = () => {
    cursor.reset()
    setSelectedId(null)
    setCommitted({
      messageId: messageId.trim() || undefined,
      correlationId: correlationId.trim() || undefined,
      serverId: serverId.trim() || undefined,
      playerUuid: playerUuid.trim() || undefined,
      status: status === 'all' ? undefined : status,
      targetKind: targetKind === 'all' ? undefined : targetKind,
      windowKey,
      cold,
    })
  }

  const query = useQuery({
    queryKey: ['messages', 'list', committed, cursor.cursor],
    queryFn: () => {
      const c = committed
      const q: MessagesQuery = {
        messageId: c.messageId,
        correlationId: c.correlationId,
        serverId: c.serverId,
        playerUuid: c.playerUuid,
        status: c.status,
        targetKind: c.targetKind,
        cursor: cursor.cursor === '' ? undefined : cursor.cursor,
        limit: PAGE_SIZE,
        includeArchived: c.cold ? true : undefined,
      }
      // 精确 ID 直查免时间范围；条件查询按预设窗口自「现在」往前推
      if (c.messageId === undefined && c.correlationId === undefined) {
        const to = Date.now()
        q.from = new Date(to - WINDOW_MS[c.windowKey]).toISOString()
        q.to = new Date(to).toISOString()
      }
      return fetchMessages(q)
    },
    placeholderData: keepPreviousData,
  })

  const rows = query.data?.items ?? []
  const nextCursor = query.data?.nextCursor ?? null
  const selected = rows.find((r) => r.messageId === selectedId) ?? null
  const dash = t('observability.messages.dash')

  // 详情为固定层抽屉，主表宽度恒定，列集不再随选中态裁剪
  const columns = useMemo<DataTableColumn<MessageItem>[]>(
    () => [
      {
        header: t('observability.messages.columns.createdAt'),
        cell: (row) => (
          <span className="tabular-nums text-xs text-ink-3">{new Date(row.createdAt).toLocaleString()}</span>
        ),
      },
      {
        header: t('observability.messages.columns.msgType'),
        cell: (row) => <span className="font-mono text-xs text-ink-1">{row.msgType}</span>,
      },
      {
        header: t('observability.messages.columns.route'),
        cell: (row) => (
          <span className="font-mono text-xs text-ink-2">
            {row.sourceServerId} → {row.resolvedServerId ?? row.targetServerId ?? row.targetPlayer ?? dash}
          </span>
        ),
      },
      {
        header: t('observability.messages.columns.targetKind'),
        cell: (row) => (
          <Badge variant={row.targetKind === 'broadcast' ? 'brand' : 'secondary'}>
            {t(`observability.messages.targetKind.${row.targetKind}`)}
          </Badge>
        ),
      },
      {
        header: t('observability.messages.columns.status'),
        cell: (row) => (
          <Badge variant={statusTone(row.status)}>{t(`observability.messages.status.${row.status}`)}</Badge>
        ),
      },
      {
        header: t('observability.messages.columns.duration'),
        cell: (row) => (
          <span className="tabular-nums text-xs text-ink-3">
            {row.durationMs === null ? dash : t('observability.messages.durationMs', { count: row.durationMs })}
          </span>
        ),
      },
    ],
    [t, dash],
  )

  const toolbar = (
    <div className="grid gap-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <span className="mr-1 flex items-center gap-2 text-[13px] font-semibold text-ink-1">
          <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
            <MessagesSquare className="size-[15px]" />
          </span>
          {t('observability.messages.listTitle')}
        </span>
        <span className="text-xs text-ink-4">{t('observability.messages.guardHint')}</span>
      </div>
      <div className="flex flex-wrap items-end gap-2">
        <QueryField label={t('observability.messages.filterMessageId')}>
          <Input
            aria-label={t('observability.messages.filterMessageId')}
            placeholder={t('observability.messages.filterMessageId')}
            value={messageId}
            onChange={(e) => {
              setMessageId(e.target.value)
            }}
            className="w-60 font-mono"
          />
        </QueryField>
        <QueryField label={t('observability.messages.filterCorrelationId')}>
          <Input
            aria-label={t('observability.messages.filterCorrelationId')}
            placeholder={t('observability.messages.filterCorrelationId')}
            value={correlationId}
            onChange={(e) => {
              setCorrelationId(e.target.value)
            }}
            className="w-60 font-mono"
          />
        </QueryField>
        <QueryField label={t('observability.messages.filterServer')}>
          <Input
            aria-label={t('observability.messages.filterServer')}
            placeholder={t('observability.messages.filterServer')}
            value={serverId}
            onChange={(e) => {
              setServerId(e.target.value)
            }}
            className="w-44"
            disabled={exactMode}
          />
        </QueryField>
        <QueryField label={t('observability.messages.filterPlayer')}>
          <Input
            aria-label={t('observability.messages.filterPlayer')}
            placeholder={t('observability.messages.filterPlayer')}
            value={playerUuid}
            onChange={(e) => {
              setPlayerUuid(e.target.value)
            }}
            className="w-60 font-mono"
            disabled={exactMode}
          />
        </QueryField>
        <QueryField label={t('observability.messages.filterStatus')}>
          <FilterSelect
            label={t('observability.messages.filterStatus')}
            value={status}
            options={STATUSES.map((v) => ({ value: v, label: t(`observability.messages.status.${v}`) }))}
            onChange={setStatus}
          />
        </QueryField>
        <QueryField label={t('observability.messages.filterTargetKind')}>
          <FilterSelect
            label={t('observability.messages.filterTargetKind')}
            value={targetKind}
            options={TARGET_KINDS.map((v) => ({ value: v, label: t(`observability.messages.targetKind.${v}`) }))}
            onChange={setTargetKind}
          />
        </QueryField>
        <QueryField label={t('observability.messages.filterWindow')}>
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
          {t('observability.messages.search')}
        </Button>
      </div>
    </div>
  )

  return (
    <section className="grid gap-5">
      <PageHeader
        icon={<MessagesSquare className="size-5" />}
        title={t('nav.messages')}
        description={t('observability.messages.mission')}
      />
      <MasterDetail
        master={
          <ListCard
            toolbar={toolbar}
            footer={
              nextCursor !== null || cursor.canPrev ? (
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
            <AsyncSection
              isLoading={query.isLoading}
              isError={query.isError}
              error={query.error}
              skeleton={<TableSkeleton columns={columns.length} rows={8} />}
            >
              <DataTable
                columns={columns}
                rows={rows}
                rowKey={(row) => row.messageId}
                emptyText={t('observability.messages.listEmpty')}
                density="compact"
                onRowClick={(row) => {
                  setSelectedId(row.messageId)
                }}
                rowClassName={(row) => (row.messageId === selectedId ? 'bg-brand-50/60' : undefined)}
              />
            </AsyncSection>
          </ListCard>
        }
        detail={selected === null ? null : <MessageDetailPanel row={selected} />}
        detailTitle={t('observability.messages.detailTitle')}
        closeLabel={t('observability.common.close')}
        onClose={() => {
          setSelectedId(null)
        }}
      />
    </section>
  )
}

// 消息详情面板：正文全部由列表行数据直显（永不空白，与连接详情同范式）；
// 仅逐跳链路 / 关联消息为详情端点独有，异步增强、带独立加载 / 错误态。
function MessageDetailPanel({ row }: { row: MessageItem }) {
  const { t } = useTranslation()
  const [payloadOpen, setPayloadOpen] = useState(false)
  const dash = t('observability.messages.dash')

  return (
    <div className="grid gap-3 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={statusTone(row.status)}>{t(`observability.messages.status.${row.status}`)}</Badge>
        <Badge variant={row.targetKind === 'broadcast' ? 'brand' : 'secondary'}>
          {t(`observability.messages.targetKind.${row.targetKind}`)}
        </Badge>
        <span className="font-mono text-xs text-ink-3">{row.msgType}</span>
      </div>

      <dl className="grid gap-1.5">
        {(
          [
            ['messageId', row.messageId],
            ['correlationId', row.correlationId ?? dash],
            ['source', row.sourceServerId],
            ['target', row.targetServerId ?? row.targetPlayer ?? dash],
            ['resolved', row.resolvedServerId ?? dash],
            [
              'crossNamespace',
              row.crossNamespace
                ? t('observability.serviceAnalysis.yes')
                : t('observability.serviceAnalysis.no'),
            ],
            ['failReason', row.failReason ?? dash],
            ['createdAt', new Date(row.createdAt).toLocaleString()],
            ['dispatchedAt', row.dispatchedAt === null ? dash : new Date(row.dispatchedAt).toLocaleString()],
            ['deliveredAt', row.deliveredAt === null ? dash : new Date(row.deliveredAt).toLocaleString()],
            [
              'duration',
              row.durationMs === null ? dash : t('observability.messages.durationMs', { count: row.durationMs }),
            ],
            ['payloadSize', `${String(row.payloadSize)} B`],
          ] as [string, string][]
        ).map(([key, value]) => (
          <div key={key} className="flex items-baseline justify-between gap-3">
            <dt className="shrink-0 text-xs text-ink-4">{t(`observability.messages.fields.${key}`)}</dt>
            <dd className="truncate text-right font-mono text-xs text-ink-1" title={value}>
              {value}
            </dd>
          </div>
        ))}
        {row.targetKind === 'broadcast' && row.fanoutTotal !== undefined && (
          <div className="flex items-baseline justify-between gap-3">
            <dt className="shrink-0 text-xs text-ink-4">{t('observability.messages.fields.broadcast')}</dt>
            <dd className="text-right text-xs text-ink-1">
              {t('observability.messages.fields.broadcastCounts', {
                total: row.fanoutTotal,
                delivered: row.deliveredCount ?? 0,
                failed: row.failedCount ?? 0,
                expired: row.expiredCount ?? 0,
              })}
            </dd>
          </div>
        )}
      </dl>

      <MessageHops messageId={row.messageId} />

      {/* payload 受控查看（原因必填 + 先审计后返回；未存储则禁入口） */}
      <div className="border-t border-border pt-2.5">
        {row.payloadStored ? (
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              setPayloadOpen(true)
            }}
          >
            {t('observability.messages.viewPayload')}
          </Button>
        ) : (
          <span className="text-xs text-ink-4">{t('observability.messages.payloadNotStored')}</span>
        )}
      </div>
      {payloadOpen && (
        <PayloadDialog
          messageId={row.messageId}
          onClose={() => {
            setPayloadOpen(false)
          }}
        />
      )}
    </div>
  )
}

// 逐跳链路 + 关联消息（详情端点独有数据）：异步增强区，加载 / 错误只影响本区、不拖垮整个面板。
function MessageHops({ messageId }: { messageId: string }) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['messages', 'detail', messageId],
    queryFn: () => fetchMessageDetail(messageId),
  })
  const detail = query.data

  return (
    <div className="border-t border-border pt-2.5">
      <p className="mb-1.5 text-xs font-semibold text-ink-2">{t('observability.messages.hopsTitle')}</p>
      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<Skeleton className="h-16 w-full" />}
      >
        {detail ? (
          <>
            <ol className="grid gap-1">
              {detail.hops.map((hop) => (
                <li key={hop.seq} className="flex items-center gap-2 text-xs">
                  <span className="w-4 shrink-0 text-right tabular-nums text-ink-4">{hop.seq}</span>
                  <Badge variant={hop.event === 'failed' ? 'crit' : 'off'}>
                    {t(`observability.messages.hopEvent.${hop.event}`)}
                  </Badge>
                  <span className="min-w-0 flex-1 truncate font-mono text-ink-2">{hop.node}</span>
                  <span className="shrink-0 tabular-nums text-ink-4">
                    {new Date(hop.at).toLocaleTimeString()}
                    {hop.costMs !== undefined && ` +${String(hop.costMs)}ms`}
                  </span>
                </li>
              ))}
            </ol>
            {/* 关联消息（correlationId RPC 往返） */}
            {detail.correlated !== null && (
              <div className="mt-2.5 border-t border-border pt-2.5">
                <p className="mb-1.5 text-xs font-semibold text-ink-2">
                  {t('observability.messages.correlatedTitle')}
                </p>
                <div className="flex items-center gap-2 text-xs">
                  <Badge variant={statusTone(detail.correlated.status)}>
                    {t(`observability.messages.status.${detail.correlated.status}`)}
                  </Badge>
                  <span className="font-mono text-ink-2">{detail.correlated.msgType}</span>
                  <span className="min-w-0 flex-1 truncate font-mono text-ink-4">{detail.correlated.messageId}</span>
                </div>
              </div>
            )}
          </>
        ) : (
          <span className="text-xs text-ink-4">{t('observability.messages.dash')}</span>
        )}
      </AsyncSection>
    </div>
  )
}
