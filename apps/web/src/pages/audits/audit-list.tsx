// 审计列表：操作人 / 动作 / 目标类型过滤 + 详情关键词搜索 + 服务端分页；行点击看详情。
// 导出按钮（CSV / JSON）在 mock 下由浏览器直接下载。

import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Download, ListFilter } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Button,
  DataTable,
  Input,
  SectionHeader,
  TableSkeleton,
  type DataTableColumn,
} from '@beacon/ui'
import type { AuditItem } from '@beacon/devmock'

import { auditExportUrl, fetchAudits } from '../../api/observability'
import FilterSelect from '../../features/observability/filter-select'
import Pager from '../../features/observability/pager'

const PAGE_SIZE = 15
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

interface AuditListProps {
  onView: (item: AuditItem) => void
}

export default function AuditList({ onView }: AuditListProps) {
  const { t } = useTranslation()
  const [operator, setOperator] = useState('all')
  const [action, setAction] = useState('all')
  const [targetType, setTargetType] = useState('all')
  const [keyword, setKeyword] = useState('')
  const [page, setPage] = useState(1)

  const query = useQuery({
    queryKey: ['audits', 'list', operator, action, targetType, keyword, page],
    queryFn: () =>
      fetchAudits({
        operator: operator === 'all' ? undefined : operator,
        action: action === 'all' ? undefined : action,
        detailKeyword: keyword.trim() === '' ? undefined : keyword.trim(),
        page,
        size: PAGE_SIZE,
      }),
    placeholderData: keepPreviousData,
  })

  // 目标类型无独立后端参数，改用 action 归类做客户端二次过滤（不破坏服务端分页 total 展示）
  const rows = useMemo(() => {
    const items = query.data?.items ?? []
    if (targetType === 'all') {
      return items
    }
    return items.filter((row) => row.targetType === targetType)
  }, [query.data, targetType])

  const total = query.data?.total ?? 0
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))

  const columns = useMemo<DataTableColumn<AuditItem>[]>(
    () => [
      {
        header: t('observability.audits.columns.time'),
        cell: (row) => <span className="tabular-nums text-xs text-ink-3">{new Date(row.createdAt).toLocaleString()}</span>,
      },
      { header: t('observability.audits.columns.operator'), cell: (row) => <span className="text-ink-2">{row.operator}</span> },
      {
        header: t('observability.audits.columns.action'),
        cell: (row) => <span className="font-mono text-xs text-ink-2">{row.action}</span>,
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
    [t],
  )

  return (
    <section className="grid gap-3">
      <SectionHeader
        icon={<ListFilter className="size-4" />}
        title={t('observability.audits.listTitle')}
        count={total > 0 ? t('observability.common.total', { count: total }) : undefined}
        actions={
          <>
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
          </>
        }
      />
      <div className="flex flex-wrap items-center gap-2">
        <Input
          aria-label={t('observability.audits.filterKeyword')}
          placeholder={t('observability.audits.filterKeyword')}
          value={keyword}
          onChange={(e) => {
            setKeyword(e.target.value)
            setPage(1)
          }}
          className="w-52"
        />
        <FilterSelect
          label={t('observability.audits.filterOperator')}
          value={operator}
          options={OPERATORS.map((v) => ({ value: v, label: v }))}
          onChange={(value) => {
            setOperator(value)
            setPage(1)
          }}
        />
        <FilterSelect
          label={t('observability.audits.filterAction')}
          value={action}
          options={ACTIONS.map((v) => ({ value: v, label: v }))}
          onChange={(value) => {
            setAction(value)
            setPage(1)
          }}
        />
        <FilterSelect
          label={t('observability.audits.filterTargetType')}
          value={targetType}
          options={TARGET_TYPES.map((v) => ({ value: v, label: v }))}
          onChange={(value) => {
            setTargetType(value)
            setPage(1)
          }}
        />
      </div>

      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<TableSkeleton columns={columns.length} rows={8} />}
      >
        <div className="overflow-hidden rounded-xl border border-border bg-card shadow-card">
          <DataTable
            columns={columns}
            rows={rows}
            rowKey={(row) => String(row.id)}
            emptyText={t('observability.audits.listEmpty')}
            density="compact"
            onRowClick={onView}
          />
        </div>
      </AsyncSection>

      {total > PAGE_SIZE && (
        <Pager page={page} pageCount={pageCount} total={total} onPageChange={setPage} />
      )}
    </section>
  )
}
