// 单服级状态墙（FR-254 / FR-270 / FR-271）：
//   - 逐台展示正推状态、回滚状态、失败原因（error / rollbackError）、备份标记与**当前交付版本**（FR-271）；
//   - 可选勾选（仅可回滚状态的单）→「回滚选中目标」（FR-270）：只回滚选中目标的文件、配置版本不回退、
//     单主状态不变；全选等价整单回滚（弹窗内明示将含配置版本回退）；确认弹窗内带回滚预检（FR-255r）；
//   - 支持按批次筛选后再勾选；执行中 5s 自动刷新，终态不轮询。
import { useEffect, useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'

import { RotateCcw, Server } from 'lucide-react'

import { AsyncSection, Badge, Button, DataTable, SectionHeader, type DataTableColumn } from '@beacon/ui'
import type { ChangeTarget } from '@beacon/contracts'

import { ApiClientError } from '../../api/delivery'
import {
  fetchChangeTargets,
  rollbackChangeTargets,
  type ChangeOrderStatus,
} from '../../api/delivery-changes'
import { randomId } from '../../lib/random-id'
import { useApprovalTicketFeedback } from './approval-ticket'
import RollbackDialog from './rollback-dialog'
import Pager from './pager'
import { TargetStatusBadge } from './status-badges'
import { formatTime } from './format'

const TARGET_PAGE_SIZE = 20

// 执行中的单状态：状态墙自动刷新（草稿 / 待审批 / 终态无进度可看，不轮询）
const LIVE_STATUSES = new Set<ChangeOrderStatus>(['rolling', 'paused', 'rolling_back'])

// 允许发起目标级回滚的单状态（与整单回滚同集：只有曾推送过的单才有可回滚目标）
const ROLLBACKABLE = new Set<ChangeOrderStatus>(['completed', 'paused', 'cancelled'])

interface TargetStatusWallProps {
  orderId: number
  /** 单状态：决定是否自动刷新与是否可勾选回滚 */
  orderStatus: ChangeOrderStatus
  /** 批次号列表（可选）：用于「按批筛选后再勾选」；缺失即不显示筛选下拉 */
  batches?: number[]
}

export default function TargetStatusWall({ orderId, orderStatus, batches }: TargetStatusWallProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const notifyTicket = useApprovalTicketFeedback()
  const [page, setPage] = useState(1)
  const [batchNo, setBatchNo] = useState(0)
  const [selected, setSelected] = useState<string[]>([])
  const [dialogOpen, setDialogOpen] = useState(false)
  const [errorText, setErrorText] = useState<string | null>(null)
  const keyRef = useState(() => ({ value: null as string | null }))[0]

  const canSelect = ROLLBACKABLE.has(orderStatus)

  const query = useQuery({
    queryKey: ['change-orders', 'targets', orderId, page, batchNo],
    // 未选批次时不带 batch 参数（而不是 batch=0）：语义是「不筛」而非「筛第 0 批」。
    queryFn: () =>
      fetchChangeTargets(orderId, {
        page,
        pageSize: TARGET_PAGE_SIZE,
        ...(batchNo > 0 ? { batch: batchNo } : {}),
      }),
    placeholderData: keepPreviousData,
    refetchInterval: LIVE_STATUSES.has(orderStatus) ? 5000 : false,
  })

  const items = query.data?.items ?? []
  const total = query.data?.total ?? 0
  const failedCount = items.filter((row) => row.status === 'failed').length
  const rollbackFailedCount = items.filter((row) => row.rollbackStatus === 'failed').length

  // 切单或筛选变化时清空勾选：跨批次的残留勾选会让「已选 N 台」与实际提交范围对不上。
  useEffect(() => {
    setSelected([])
  }, [orderId, batchNo])

  const selectableOnPage = useMemo(
    () => items.filter((row) => row.pushedAt != null).map((row) => row.serverId),
    [items],
  )
  const allPageSelected = selectableOnPage.length > 0 && selectableOnPage.every((id) => selected.includes(id))
  // 全选等价整单回滚（/spec §3.3）：勾满全部可回滚目标时按钮与提示都切到整单口径，避免按子集预期操作。
  const allSelected = selected.length > 0 && selected.length === total

  const toggleOne = (serverId: string, checked: boolean) => {
    setSelected((prev) => (checked ? [...new Set([...prev, serverId])] : prev.filter((id) => id !== serverId)))
  }

  const mutation = useMutation({
    mutationFn: (reason: string) => {
      const key = keyRef.value ?? randomId()
      keyRef.value = key
      return rollbackChangeTargets(orderId, selected, reason, key)
    },
    onSuccess: async (ticket) => {
      keyRef.value = null
      setDialogOpen(false)
      setSelected([])
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['change-orders'] }),
        queryClient.invalidateQueries({ queryKey: ['approvals'] }),
      ])
      notifyTicket(ticket)
    },
    onError: (error) => {
      setErrorText(error instanceof ApiClientError ? error.message : String(error))
    },
  })

  const columns = useMemo<DataTableColumn<ChangeTarget>[]>(() => {
    const base: DataTableColumn<ChangeTarget>[] = []
    if (canSelect) {
      base.push({
        header: t('delivery.changes.detail.targets.columns.select'),
        cell: (row) => {
          const rollbackable = row.pushedAt != null
          return (
            <input
              type="checkbox"
              className="size-4 align-middle"
              aria-label={row.serverId}
              checked={selected.includes(row.serverId)}
              disabled={!rollbackable}
              title={rollbackable ? undefined : t('delivery.changes.detail.targets.noRollbackTarget')}
              onChange={(e) => {
                toggleOne(row.serverId, e.target.checked)
              }}
            />
          )
        },
      })
    }
    base.push(
      {
        header: t('delivery.changes.detail.targets.columns.serverId'),
        cell: (row) => <span className="font-mono">{row.serverId}</span>,
      },
      {
        header: t('delivery.changes.detail.targets.columns.batch'),
        cell: (row) => <span className="tnum">#{String(row.batchNo)}</span>,
      },
      {
        header: t('delivery.changes.detail.targets.columns.status'),
        cell: (row) => <TargetStatusBadge status={row.status} />,
      },
      {
        header: t('delivery.changes.detail.targets.columns.rollback'),
        cell: (row) =>
          row.rollbackStatus === null ? (
            <span className="text-ink-4">{t('delivery.changes.detail.targets.rollbackNone')}</span>
          ) : (
            <Badge variant={row.rollbackStatus === 'failed' ? 'crit' : 'ok'} className="gap-1.5">
              <span className="size-1.5 rounded-full bg-current" aria-hidden />
              {t(`delivery.changes.detail.targets.rollbackStatus.${row.rollbackStatus}`)}
            </Badge>
          ),
      },
      {
        // 当前交付版本（FR-271）：该服最近一条 activated 且未被回滚的交付记录；无记录显示「无交付记录」。
        // 记录归属于某一变更单，故给出到该单的可跳转入口（MU 场景下「这台是谁交付的」接着就能点开看）。
        header: t('delivery.changes.detail.targets.columns.delivered'),
        cell: (row) =>
          row.deliveredVersion == null ? (
            <span className="text-ink-4">{t('delivery.changes.detail.targets.deliveredVersionNone')}</span>
          ) : (
            <Link
              className="text-brand-600 hover:underline"
              to={`/changes?order=${String(row.deliveredVersion.orderId)}`}
              title={row.deliveredVersion.orderTitle}
            >
              {t('delivery.changes.detail.targets.deliveredVersionHint', {
                orderId: row.deliveredVersion.orderId,
                title: row.deliveredVersion.orderTitle,
                at: formatTime(row.deliveredVersion.activatedAt),
              })}
            </Link>
          ),
      },
      {
        header: t('delivery.changes.detail.targets.columns.error'),
        cell: (row) => <FailureReason text={row.error} />,
      },
      {
        header: t('delivery.changes.detail.targets.columns.rollbackError'),
        cell: (row) => <FailureReason text={row.rollbackError} />,
      },
      {
        header: t('delivery.changes.detail.targets.columns.backup'),
        cell: (row) =>
          row.backupPresent ? (
            <Badge variant="ok" className="gap-1.5">
              <span className="size-1.5 rounded-full bg-current" aria-hidden />
              {t('delivery.changes.detail.targets.backupPresent')}
            </Badge>
          ) : (
            <span className="text-ink-4">{t('delivery.changes.detail.targets.backupMissing')}</span>
          ),
      },
    )
    return base
  }, [t, canSelect, selected])

  return (
    <div className="grid gap-2">
      <SectionHeader
        icon={<Server className="size-4" />}
        title={t('delivery.changes.detail.targets.title')}
      />
      <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
        <div className="grid gap-2">
          {/* 汇总一行：全单总台数 + 本页失败 / 回滚失败计数（计数只覆盖当前页，故显式标「本页」） */}
          <p className="text-xs text-ink-3">
            {t('delivery.changes.detail.targets.summary', { total })}
            {failedCount > 0 &&
              ` · ${t('delivery.changes.detail.targets.failedCount', { count: failedCount })}`}
            {rollbackFailedCount > 0 &&
              ` · ${t('delivery.changes.detail.targets.rollbackFailedCount', { count: rollbackFailedCount })}`}
          </p>

          {/* 目标级回滚工具条（FR-270）：按批筛选 → 勾选 → 回滚选中目标 */}
          {canSelect && (
            <div className="flex flex-wrap items-center gap-2 text-xs">
              {batches != null && batches.length > 1 && (
                <label className="flex items-center gap-1.5 text-ink-3">
                  {t('delivery.changes.detail.targets.columns.batch')}
                  <select
                    className="rounded border border-line-1 bg-transparent px-1.5 py-0.5"
                    aria-label={t('delivery.changes.detail.targets.columns.batch')}
                    value={String(batchNo)}
                    onChange={(e) => {
                      setBatchNo(Number(e.target.value))
                    }}
                  >
                    <option value="0">—</option>
                    {batches.map((no) => (
                      <option key={no} value={String(no)}>
                        #{no}
                      </option>
                    ))}
                  </select>
                </label>
              )}
              <Button
                variant="outline"
                size="sm"
                onClick={() => {
                  setSelected(allPageSelected ? [] : selectableOnPage)
                }}
                disabled={selectableOnPage.length === 0}
              >
                {allPageSelected
                  ? t('delivery.changes.detail.targets.clearSelection')
                  : t('delivery.changes.detail.targets.selectAll')}
              </Button>
              <span className="text-ink-2">
                {t('delivery.changes.detail.targets.selected', { count: selected.length })}
              </span>
              <Button
                variant="destructive"
                size="sm"
                disabled={selected.length === 0}
                onClick={() => {
                  setErrorText(null)
                  setDialogOpen(true)
                }}
              >
                <RotateCcw className="size-4" />
                {allSelected
                  ? t('delivery.changes.detail.targets.rollbackSelectedAll')
                  : t('delivery.changes.detail.targets.rollbackSelected')}
              </Button>
              <span className="text-ink-4">{t('delivery.changes.detail.targets.selectHint')}</span>
              {errorText !== null && !dialogOpen && <span className="text-crit">{errorText}</span>}
            </div>
          )}

          <DataTable
            columns={columns}
            rows={items}
            rowKey={(row) => `${row.serverId}:${String(row.batchNo)}`}
            emptyText={t('delivery.changes.detail.batches.empty')}
            density="compact"
          />
          <Pager page={page} total={total} pageSize={TARGET_PAGE_SIZE} onPageChange={setPage} />
        </div>
      </AsyncSection>

      {/* 目标级回滚确认：明示「只回滚选中台的文件、配置版本不回退」+ 回滚预检（FR-255r） */}
      <RollbackDialog
        open={dialogOpen}
        pending={mutation.isPending}
        errorText={mutation.isError ? errorText : null}
        textKey="rollback.targets"
        selectedCount={selected.length}
        allSelected={allSelected}
        precheck={precheckForSelection(items, selected)}
        onConfirm={(reason) => {
          mutation.mutate(reason)
        }}
        onOpenChange={(open) => {
          setDialogOpen(open)
          if (!open) {
            keyRef.value = null
            setErrorText(null)
          }
        }}
      />
    </div>
  )
}

// precheckForSelection 由**当前页**目标行推导选中集合里的预检：只对已读到的行给结论，
// 选中集合里有当前页外的台时整体标 unavailable——宁可说「预检不可用」，也不把没读到当「可回滚」。
function precheckForSelection(
  items: ChangeTarget[],
  selected: string[],
): { unavailable: boolean; blocked: string[]; checked: number; total: number } | null {
  if (selected.length === 0) {
    return null
  }
  const known = new Map(items.map((row) => [row.serverId, row]))
  const unknown = selected.filter((id) => !known.has(id))
  const blocked = selected.filter((id) => known.get(id)?.backupPresent === false)
  return {
    unavailable: unknown.length > 0,
    blocked,
    checked: selected.length - unknown.length,
    total: selected.length,
  }
}

// 失败原因单元格：为空显示占位符，非空按危险色展示并保留全文 tooltip（长文案不截断语义）
function FailureReason({ text }: { text: string | null }) {
  const { t } = useTranslation()
  if (text === null || text === '') {
    return <span className="text-ink-4">{t('delivery.changes.detail.targets.noFailure')}</span>
  }
  return (
    <span className="text-crit" title={text}>
      {text}
    </span>
  )
}
