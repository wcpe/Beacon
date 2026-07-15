// 审计列表（主列）：吸顶工具条（关键词 / 目标 + 操作人 / 动作 / 目标类型筛选 + 导出）+ 自区滚动列表 + 吸底分页。
// 行点击回调交父级用右侧非模态详情面板承载；选中行高亮。导出按钮在吸顶工具区始终可见。
// 筛选初值消费 URL 查询参数（targetRef/action/operator/targetType），承接 /commands、/alert-events、
// 连接消息查询面等页的互跳链接（FR-157，含 action=message.payload.view 定位 payload 查看审计）；
// 页内变更筛选不回写 URL（最简策略）。

import { useCallback, useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router-dom'
import { Download, ListFilter } from 'lucide-react'

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
import type { AuditItem } from '@beacon/contracts'

import { auditExportUrl, fetchAudits } from '../../api/observability'
import FilterSelect from '../../features/observability/filter-select'
import ListCard from '../../features/shared/list-card'
import Pager from '../../features/observability/pager'
import CursorPager from '../../features/observability/cursor-pager'
import { useCursorStack } from '../../features/observability/use-cursor-stack'

const PAGE_SIZE = 15
// 时间范围预设 key → 毫秒跨度（'all' = 不限时间，仅热查询可用；冷查询强制有界且 ≤31 天上限）
const AUDIT_WINDOW_MS: Record<string, number> = {
  '24h': 86_400_000,
  '7d': 604_800_000,
  '30d': 2_592_000_000,
}
const OPERATORS = ['admin', 'ops-chen', 'ops-wang', 'system'] as const
// 与 devmock AUDIT_ACTIONS 对齐（动作 / 目标类型枚举）
const ACTIONS = [
  'identity.approved',
  'identity.rejected',
  'zone.rezone.initiated',
  'config.file.trash',
  'delivery.order.start',
  'delivery.order.batch_confirm',
  'cross_namespace.schedule',
  'message.payload.view',
  'asset.preview',
  'settings.update',
  'namespace_trust.grant',
] as const
const TARGET_TYPES = [
  'agent-identity',
  'server',
  'config-file',
  'change-order',
  'namespace-trust',
  'message',
  'file-asset',
  'setting',
] as const

// 从 URL 查询参数取下拉筛选初值：非空即采纳（审计动作 / 目标类型随后端演进，未知值也按原样过滤），
// 缺省回退「全部」。
function initialParam(params: URLSearchParams, name: string): string {
  const value = params.get(name)
  return value !== null && value !== '' ? value : 'all'
}

// 下拉候选集：URL 带入的未知取值动态并入候选首位，保证触发器可回显当前筛选值
function withCurrent(options: readonly string[], current: string): readonly string[] {
  if (current === 'all' || options.includes(current)) {
    return options
  }
  return [current, ...options]
}

interface AuditListProps {
  onView: (item: AuditItem) => void
  // 当前选中行 id（高亮用）
  selectedId: number | null
}

export default function AuditList({ onView, selectedId }: AuditListProps) {
  const { t } = useTranslation()
  // 审计动作中文标签：有映射用中文，未映射经 defaultValue 回退原始枚举（防裸 key 同时不挡未知动作）
  const actionLabel = useCallback(
    (action: string): string => t(`observability.audits.action.${action}`, { defaultValue: action }),
    [t],
  )
  // 互跳承接：以 URL 查询参数为筛选初值（仅初始化，页内变更不回写 URL）
  const [searchParams] = useSearchParams()
  const [operator, setOperator] = useState(() => initialParam(searchParams, 'operator'))
  const [action, setAction] = useState(() => initialParam(searchParams, 'action'))
  const [targetType, setTargetType] = useState(() => initialParam(searchParams, 'targetType'))
  const [targetRef, setTargetRef] = useState(() => searchParams.get('targetRef') ?? '')
  const [keyword, setKeyword] = useState('')
  const [page, setPage] = useState(1)
  // 时间范围预设（'all' = 不限时间，保持原有全量行为）与冷查询开关（FR-152）
  const [windowKey, setWindowKey] = useState('all')
  const [cold, setCold] = useState(false)
  const cursor = useCursorStack()

  // 切换过滤 / 时间窗 / 冷查询开关时回到首页（热重置页码、冷重置游标栈）
  const resetPaging = () => {
    setPage(1)
    cursor.reset()
  }

  const query = useQuery({
    queryKey: ['audits', 'list', operator, action, targetType, targetRef, keyword, windowKey, cold, cold ? cursor.cursor : String(page)],
    queryFn: () => {
      // 时间范围按预设窗口自「现在」往前推（RFC3339）；'all' 不带 from/to（仅热查询可达）
      const span = windowKey === 'all' ? undefined : AUDIT_WINDOW_MS[windowKey]
      const to = Date.now()
      return fetchAudits({
        operator: operator === 'all' ? undefined : operator,
        action: action === 'all' ? undefined : action,
        // 目标类型 / 目标为真后端原生查询参数（audit_handler.go List），走服务端过滤
        targetType: targetType === 'all' ? undefined : targetType,
        targetRef: targetRef.trim() === '' ? undefined : targetRef.trim(),
        detailKeyword: keyword.trim() === '' ? undefined : keyword.trim(),
        from: span === undefined ? undefined : new Date(to - span).toISOString(),
        to: span === undefined ? undefined : new Date(to).toISOString(),
        page,
        size: PAGE_SIZE,
        includeArchived: cold ? true : undefined,
        cursor: cold ? cursor.cursor : undefined,
      })
    },
    placeholderData: keepPreviousData,
  })

  const rows = query.data?.items ?? []

  const total = query.data?.total ?? 0
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const nextCursor = query.data?.nextCursor ?? null

  const columns = useMemo<DataTableColumn<AuditItem>[]>(
    () => [
      {
        header: t('observability.audits.columns.time'),
        cell: (row) => <span className="tabular-nums text-xs text-ink-3">{new Date(row.createdAt).toLocaleString()}</span>,
      },
      { header: t('observability.audits.columns.operator'), cell: (row) => <span className="text-ink-2">{row.operator}</span> },
      {
        header: t('observability.audits.columns.action'),
        cell: (row) => <span className="font-mono text-xs text-ink-2">{actionLabel(row.action)}</span>,
      },
      { header: t('observability.audits.columns.targetType'), cell: (row) => <span className="text-ink-3">{row.targetType}</span> },
      {
        header: t('observability.audits.columns.targetRef'),
        cell: (row) => <span className="font-mono text-xs text-ink-2">{row.targetRef}</span>,
      },
      {
        header: t('observability.audits.columns.result'),
        cell: (row) => (
          <Badge variant={row.result === 'ok' ? 'ok' : 'crit'}>
            {t(`observability.audits.result.${row.result}`)}
          </Badge>
        ),
      },
    ],
    [t, actionLabel],
  )

  const toolbar = (
    <div className="grid gap-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <span className="mr-1 flex items-center gap-2 text-[13px] font-semibold text-ink-1">
          <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
            <ListFilter className="size-[15px]" />
          </span>
          {t('observability.audits.listTitle')}
        </span>
        {total > 0 && <span className="text-xs text-ink-3">{t('observability.common.total', { count: total })}</span>}
        <div className="ml-auto flex items-center gap-2">
          <Button size="sm" variant="outline" asChild>
            <a href={auditExportUrl('csv')} download>
              <Download className="size-3.5" />
              {t('observability.audits.exportCsv')}
            </a>
          </Button>
          <Button size="sm" variant="outline" asChild>
            <a href={auditExportUrl('json')} download>
              <Download className="size-3.5" />
              {t('observability.audits.exportJson')}
            </a>
          </Button>
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Input
          aria-label={t('observability.audits.filterKeyword')}
          placeholder={t('observability.audits.filterKeyword')}
          value={keyword}
          onChange={(e) => {
            setKeyword(e.target.value)
            resetPaging()
          }}
          className="w-52"
        />
        <Input
          aria-label={t('observability.audits.filterTargetRef')}
          placeholder={t('observability.audits.filterTargetRef')}
          value={targetRef}
          onChange={(e) => {
            setTargetRef(e.target.value)
            resetPaging()
          }}
          className="w-52"
        />
        <FilterSelect
          label={t('observability.audits.filterOperator')}
          value={operator}
          options={withCurrent(OPERATORS, operator).map((v) => ({ value: v, label: v }))}
          onChange={(value) => {
            setOperator(value)
            resetPaging()
          }}
        />
        <FilterSelect
          label={t('observability.audits.filterAction')}
          value={action}
          options={withCurrent(ACTIONS, action).map((v) => ({ value: v, label: actionLabel(v) }))}
          onChange={(value) => {
            setAction(value)
            resetPaging()
          }}
        />
        <FilterSelect
          label={t('observability.audits.filterTargetType')}
          value={targetType}
          options={withCurrent(TARGET_TYPES, targetType).map((v) => ({ value: v, label: v }))}
          onChange={(value) => {
            setTargetType(value)
            resetPaging()
          }}
        />
        <FilterSelect
          label={t('observability.audits.filterWindow')}
          value={windowKey}
          options={Object.keys(AUDIT_WINDOW_MS).map((key) => ({
            value: key,
            label: t(`observability.audits.window${key}`),
          }))}
          onChange={(value) => {
            setWindowKey(value)
            // 冷查询强制有界时间范围：选回「全部」时自动退出冷查询（避免 400 死角）
            if (value === 'all') {
              setCold(false)
            }
            resetPaging()
          }}
        />
        <label
          className="flex cursor-pointer items-center gap-2 text-sm text-ink-2"
          title={t('observability.common.includeArchivedHint')}
        >
          <Checkbox
            checked={cold}
            onCheckedChange={(v) => {
              const next = v === true
              setCold(next)
              // 勾选时若时间范围为「全部」自动收敛到 30 天（冷查询强制有界 ≤31 天）
              if (next && windowKey === 'all') {
                setWindowKey('30d')
              }
              resetPaging()
            }}
            aria-label={t('observability.common.includeArchived')}
          />
          {t('observability.common.includeArchived')}
        </label>
      </div>
    </div>
  )

  return (
    <ListCard
      toolbar={toolbar}
      footer={
        cold
          ? nextCursor !== null || cursor.canPrev
            ? (
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
                />
              )
            : undefined
          : total > PAGE_SIZE
            ? <Pager page={page} pageCount={pageCount} total={total} onPageChange={setPage} />
            : undefined
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
          rowKey={(row) => String(row.id)}
          emptyText={t('observability.audits.listEmpty')}
          density="compact"
          onRowClick={onView}
          rowClassName={(row) => (row.id === selectedId ? 'bg-brand-50/60' : undefined)}
        />
      </AsyncSection>
    </ListCard>
  )
}
