// MCP 工具调用流水列表（主列，FR-241）：吸顶工具条（六维筛选 + 危险操作视图）+ 自区滚动列表 + 吸底游标分页。
//
// 交互范式对齐仓库既有页（/connections、/audits）：
//   - 筛选条 = QueryField 标签 + Input / FilterSelect（'all' 表示不筛选），变更即重查并回到首页；
//   - 列表 = ListCard 自区滚动 + DataTable（空态由 DataTable 承担、加载 / 错误态交给 AsyncSection）；
//   - 分页 = 游标翻页（后端不返回 total：跨日表精确总数需全扫），复用 useCursorStack + CursorPager。
//
// 危险操作视图（PRD 硬要求）：结果 / 风险等级两个筛选维度负责收窄，「仅看危险操作」开关再把当前结果
// 收窄为被拒或关键风险——真后端的六维过滤是 AND 组合，无法在一次请求里表达「被拒 OR 关键风险」，
// 故该开关是**当前结果内的二次收窄**（文案已明示），危险行同时有整行危险底色标记。
import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ListFilter } from 'lucide-react'

import {
  AsyncSection,
  Checkbox,
  DataTable,
  Input,
  SummaryStrip,
  TableSkeleton,
  type DataTableColumn,
  type SummaryItem,
} from '@beacon/ui'
import type {
  MCPInvocationItem,
  MCPInvocationReason,
  MCPInvocationResult,
  MCPInvocationRiskLevel,
} from '@beacon/contracts'

import { fetchMCPInvocations, type MCPInvocationQuery } from '../../api/mcp'
import CursorPager from '../../features/observability/cursor-pager'
import FilterSelect from '../../features/observability/filter-select'
import QueryField from '../../features/observability/query-field'
import { useCursorStack } from '../../features/observability/use-cursor-stack'
import ListCard from '../../features/shared/list-card'
import { DANGER_ROW_CLASS, isDangerous, ReasonLabel, ResultBadge, RiskBadge } from './invocation-visuals'

// 单页条数：调用流水是低频机器操作，20 条一页足够翻读（与 /connections 同档）
const PAGE_SIZE = 20
// 筛选候选集与契约枚举一一对应（'all' 由 FilterSelect 自动前置为「全部」）
const RESULTS: readonly MCPInvocationResult[] = ['ok', 'fail', 'rejected']
const RISK_LEVELS: readonly MCPInvocationRiskLevel[] = ['low', 'high', 'critical', 'unknown']
const REASONS: readonly MCPInvocationReason[] = [
  'unknown_tool',
  'production_mode',
  'handler_rejected',
  'handler_error',
  'input_required',
  'internal_error',
]
// 时间范围预设 key → 毫秒跨度；'all' = 不限时间（不传 from/to，后端按存在日表返回）
const WINDOW_MS: Record<string, number | undefined> = {
  all: undefined,
  '1h': 3_600_000,
  '6h': 21_600_000,
  '24h': 86_400_000,
  '7d': 604_800_000,
  '30d': 2_592_000_000,
}
const WINDOW_KEYS = ['1h', '6h', '24h', '7d', '30d'] as const

interface InvocationListProps {
  // 行点击：交父级用右侧非模态详情面板承载
  onView: (row: MCPInvocationItem) => void
  // 当前选中行（高亮用）
  selectedId: string | null
}

export default function InvocationList({ onView, selectedId }: InvocationListProps) {
  const { t } = useTranslation()
  const [tool, setTool] = useState('')
  const [clientId, setClientId] = useState('')
  const [result, setResult] = useState('all')
  const [riskLevel, setRiskLevel] = useState('all')
  const [reason, setReason] = useState('all')
  const [windowKey, setWindowKey] = useState('all')
  // 危险操作视图开关（页内二次收窄，见文件头说明）
  const [dangerOnly, setDangerOnly] = useState(false)
  const cursor = useCursorStack()

  // 任一筛选变更都回到首页：游标是相对「同一组筛选条件」的 keyset 令牌，跨条件复用会读到错位数据
  const resetPaging = () => {
    cursor.reset()
  }

  const query = useQuery({
    queryKey: [
      'mcp-invocations',
      'list',
      tool,
      clientId,
      result,
      riskLevel,
      reason,
      windowKey,
      cursor.cursor,
    ],
    queryFn: () => {
      // 时间范围按预设自「现在」往前推（RFC3339，闭区间）；'all' 不带 from/to
      const span = WINDOW_MS[windowKey]
      const to = Date.now()
      const params: MCPInvocationQuery = {
        tool: tool.trim() === '' ? undefined : tool.trim(),
        clientId: clientId.trim() === '' ? undefined : clientId.trim(),
        result: result === 'all' ? undefined : (result as MCPInvocationResult),
        riskLevel: riskLevel === 'all' ? undefined : (riskLevel as MCPInvocationRiskLevel),
        reason: reason === 'all' ? undefined : (reason as MCPInvocationReason),
        from: span === undefined ? undefined : new Date(to - span).toISOString(),
        to: span === undefined ? undefined : new Date(to).toISOString(),
        cursor: cursor.cursor === '' ? undefined : cursor.cursor,
        limit: PAGE_SIZE,
      }
      return fetchMCPInvocations(params)
    },
    placeholderData: keepPreviousData,
  })

  const rows = useMemo(() => query.data?.items ?? [], [query.data])
  // 契约口径：nextCursor 为空串表示末页（不是 null）
  const nextCursor = query.data?.nextCursor ?? ''
  const dangerRows = useMemo(() => rows.filter(isDangerous), [rows])
  const visibleRows = dangerOnly ? dangerRows : rows

  // 本页汇总（后端无 total，故口径一律写「本页」，不给全局错觉）
  const summary = useMemo<SummaryItem[]>(() => {
    let rejected = 0
    let critical = 0
    let fail = 0
    for (const row of rows) {
      if (row.result === 'rejected') rejected += 1
      if (row.result === 'fail') fail += 1
      if (row.riskLevel === 'critical') critical += 1
    }
    return [
      { label: t('system.mcpInvocations.summary.rows'), value: rows.length },
      { label: t('system.mcpInvocations.summary.rejected'), value: rejected, tone: 'danger' },
      { label: t('system.mcpInvocations.summary.critical'), value: critical, tone: 'danger' },
      { label: t('system.mcpInvocations.summary.fail'), value: fail, tone: 'warning' },
    ]
  }, [rows, t])

  const columns = useMemo<DataTableColumn<MCPInvocationItem>[]>(
    () => [
      {
        header: t('system.mcpInvocations.columns.createdAt'),
        cell: (row) => (
          <span className="tabular-nums text-xs text-ink-3">{new Date(row.createdAt).toLocaleString()}</span>
        ),
      },
      {
        header: t('system.mcpInvocations.columns.toolName'),
        cell: (row) => <span className="font-mono text-xs text-ink-1">{row.toolName}</span>,
      },
      {
        header: t('system.mcpInvocations.columns.clientId'),
        cell: (row) => <span className="font-mono text-xs text-ink-2">{row.clientId}</span>,
      },
      {
        header: t('system.mcpInvocations.columns.result'),
        cell: (row) => <ResultBadge result={row.result} />,
      },
      {
        header: t('system.mcpInvocations.columns.riskLevel'),
        cell: (row) => <RiskBadge level={row.riskLevel} />,
      },
      {
        header: t('system.mcpInvocations.columns.reason'),
        cell: (row) => <ReasonLabel reason={row.reason} />,
      },
      {
        header: t('system.mcpInvocations.columns.duration'),
        cell: (row) => (
          <span className="tabular-nums text-xs text-ink-3">
            {t('system.mcpInvocations.durationMs', { count: row.durationMs })}
          </span>
        ),
      },
    ],
    [t],
  )

  const toolbar = (
    <div className="grid gap-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <span className="mr-1 flex items-center gap-2 text-[13px] font-semibold text-ink-1">
          <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
            <ListFilter className="size-[15px]" />
          </span>
          {t('system.mcpInvocations.listTitle')}
        </span>
        <span className="text-xs text-ink-4">{t('system.mcpInvocations.mission')}</span>
      </div>
      <div className="flex flex-wrap items-end gap-2">
        <QueryField label={t('system.mcpInvocations.filters.tool')}>
          <Input
            aria-label={t('system.mcpInvocations.filters.tool')}
            className="w-60 font-mono"
            placeholder={t('system.mcpInvocations.filters.toolPlaceholder')}
            value={tool}
            onChange={(e) => {
              setTool(e.target.value)
              resetPaging()
            }}
          />
        </QueryField>
        <QueryField label={t('system.mcpInvocations.filters.clientId')}>
          <Input
            aria-label={t('system.mcpInvocations.filters.clientId')}
            className="w-60 font-mono"
            placeholder={t('system.mcpInvocations.filters.clientIdPlaceholder')}
            value={clientId}
            onChange={(e) => {
              setClientId(e.target.value)
              resetPaging()
            }}
          />
        </QueryField>
        <QueryField label={t('system.mcpInvocations.filters.result')}>
          <FilterSelect
            label={t('system.mcpInvocations.filters.result')}
            options={RESULTS.map((value) => ({
              value,
              label: t(`system.mcpInvocations.result.${value}`),
            }))}
            value={result}
            onChange={(value) => {
              setResult(value)
              resetPaging()
            }}
          />
        </QueryField>
        <QueryField label={t('system.mcpInvocations.filters.riskLevel')}>
          <FilterSelect
            label={t('system.mcpInvocations.filters.riskLevel')}
            options={RISK_LEVELS.map((value) => ({
              value,
              label: t(`system.mcpInvocations.riskLevel.${value}`),
            }))}
            value={riskLevel}
            onChange={(value) => {
              setRiskLevel(value)
              resetPaging()
            }}
          />
        </QueryField>
        <QueryField label={t('system.mcpInvocations.filters.reason')}>
          <FilterSelect
            label={t('system.mcpInvocations.filters.reason')}
            options={REASONS.map((value) => ({
              value,
              label: t(`system.mcpInvocations.reason.${value}`),
            }))}
            value={reason}
            onChange={(value) => {
              setReason(value)
              resetPaging()
            }}
          />
        </QueryField>
        <QueryField label={t('system.mcpInvocations.filters.window')}>
          <FilterSelect
            label={t('system.mcpInvocations.filters.window')}
            options={WINDOW_KEYS.map((value) => ({
              value,
              label: t(`system.mcpInvocations.window.${value}`),
            }))}
            value={windowKey}
            onChange={(value) => {
              setWindowKey(value)
              resetPaging()
            }}
          />
        </QueryField>
        {/* 危险操作视图：既有 Checkbox + label 形态（同 /connections「包含归档」），不引入新交互模式 */}
        <label
          className="flex h-9 cursor-pointer items-center gap-2 text-sm text-ink-2"
          title={t('system.mcpInvocations.danger.hint')}
        >
          <Checkbox
            aria-label={t('system.mcpInvocations.danger.view')}
            checked={dangerOnly}
            onCheckedChange={(checked) => {
              setDangerOnly(checked === true)
            }}
          />
          <span className={dangerOnly ? 'font-medium text-crit' : undefined}>
            {t('system.mcpInvocations.danger.view')}
          </span>
          <span className="tabular-nums text-xs text-ink-4">{dangerRows.length}</span>
        </label>
      </div>
      {/* 开关打开时常驻口径说明：收窄是「当前筛选结果内」的二次过滤，不是服务端聚合 */}
      {dangerOnly && <p className="text-xs text-crit">{t('system.mcpInvocations.danger.hint')}</p>}
    </div>
  )

  return (
    <div className="grid gap-4">
      <SummaryStrip items={summary} />
      <ListCard
        toolbar={toolbar}
        footer={
          // 首页且无下一页时不渲染分页条（与 /connections 同口径）
          nextCursor !== '' || cursor.canPrev ? (
            <CursorPager
              canNext={nextCursor !== ''}
              canPrev={cursor.canPrev}
              cold={false}
              pageIndex={cursor.pageIndex}
              onNext={() => {
                if (nextCursor !== '') {
                  cursor.goNext(nextCursor)
                }
              }}
              onPrev={cursor.goPrev}
            />
          ) : undefined
        }
      >
        <AsyncSection
          error={query.error}
          isError={query.isError}
          isLoading={query.isLoading}
          loadingText={t('system.mcpInvocations.loadFail')}
          skeleton={<TableSkeleton columns={columns.length} rows={8} />}
        >
          <DataTable
            columns={columns}
            density="compact"
            emptyText={
              // 危险视图被开关收窄到空 与 真的没有数据 是两件事，分别给文案
              dangerOnly && rows.length > 0
                ? t('system.mcpInvocations.danger.empty')
                : t('system.mcpInvocations.empty')
            }
            rows={visibleRows}
            rowClassName={(row) => {
              // 选中高亮优先（否则选中行的品牌色会被危险底色盖掉）；其余危险行给整行标记
              if (row.invocationId === selectedId) {
                return 'bg-brand-50/60'
              }
              return isDangerous(row) ? DANGER_ROW_CLASS : undefined
            }}
            rowKey={(row) => row.invocationId}
            onRowClick={onView}
          />
        </AsyncSection>
      </ListCard>
    </div>
  )
}
