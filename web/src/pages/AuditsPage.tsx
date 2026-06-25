// 审计日志页：按 namespace/action/targetType/targetRef/时间范围过滤，分页展示（时间倒序）。

import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { exportAudits, listAudits, listNamespaces } from '../api/client'
import type { AuditExportFormat, AuditFilter } from '../api/client'
import type { AuditView } from '../api/types'
import { formatTime, namespaceOptions } from '../api/format'
import { useMessage } from '@/components/useMessage'
import AsyncSection from '@/components/AsyncSection'
import { TableSkeleton } from '@/components/skeletons'
import DataTable, { type DataTableColumn } from '@/components/DataTable'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Combobox } from '@/components/ui/combobox'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

// 单页条数（固定，运维场景无需可配）
const PAGE_SIZE = 20

// 把表单里的本地时间值转成后端可识别的 ISO（UTC）字符串；空值返回 undefined
function toIso(local: string): string | undefined {
  if (!local) return undefined
  const d = new Date(local)
  if (Number.isNaN(d.getTime())) return undefined
  return d.toISOString()
}

export default function AuditsPage() {
  const { t } = useTranslation()
  const msg = useMessage()
  // 审计 action 英文枚举 → 中文显示（i18n 映射；未知枚举回退原文，后端仍返英文枚举）
  const actionLabel = (action: string) => t(`audit.action.${action}`, { defaultValue: action })
  // 过滤表单的草稿值（点「查询」时才生效）
  const [namespace, setNamespace] = useState('')
  const [operator, setOperator] = useState('')
  const [action, setAction] = useState('')
  const [targetType, setTargetType] = useState('')
  const [targetRef, setTargetRef] = useState('')
  const [detailKeyword, setDetailKeyword] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  // 已生效的查询条件
  const [filter, setFilter] = useState<AuditFilter>({ page: 1, size: PAGE_SIZE })
  // 详情 Dialog 选中的审计条目（null 表示关闭）
  const [selectedAudit, setSelectedAudit] = useState<AuditView | null>(null)
  // 导出中标记（防重复点击；csv/json 各算一次）
  const [exporting, setExporting] = useState(false)

  const { data, isLoading, isError, error, isFetching } = useQuery({
    queryKey: ['audits', filter],
    queryFn: () => listAudits(filter),
    placeholderData: keepPreviousData,
  })

  // 环境筛选下拉的候选来源（FR-51）：来自 listNamespaces，筛选框允许键入候选外的值（可编辑）
  // 候选显示「编码 · 名称」，真实值仍是 code（FR-70）
  const namespacesQuery = useQuery({ queryKey: ['namespaces'], queryFn: () => listNamespaces() })
  const nsOptions = namespaceOptions(namespacesQuery.data)

  function onSearch(e: React.FormEvent) {
    e.preventDefault()
    setFilter({
      namespace: namespace.trim() || undefined,
      operator: operator.trim() || undefined,
      action: action.trim() || undefined,
      targetType: targetType.trim() || undefined,
      targetRef: targetRef.trim() || undefined,
      detailKeyword: detailKeyword.trim() || undefined,
      from: toIso(from),
      to: toIso(to),
      page: 1,
      size: PAGE_SIZE,
    })
  }

  function goPage(page: number) {
    setFilter((f) => ({ ...f, page }))
  }

  // 导出当前已生效过滤条件下的全量审计（不分页，FR-84）；后端流式输出 CSV/JSON。
  async function onExport(format: AuditExportFormat) {
    if (exporting) return
    setExporting(true)
    try {
      await exportAudits(filter, format)
    } catch (e) {
      msg.showError(`${t('audit.exportFailed')}：${(e as Error).message}`)
    } finally {
      setExporting(false)
    }
  }

  const total = data?.total ?? 0
  const page = filter.page ?? 1
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  // 审计表列定义（详情列闭包引用 setSelectedAudit，故在组件内定义）
  const columns: DataTableColumn<AuditView>[] = [
    { header: t('audit.colTime'), cell: (a) => formatTime(a.createdAt) },
    { header: t('audit.colNamespace'), cell: (a) => a.namespace || '-' },
    { header: t('audit.colOperator'), cell: (a) => a.operator },
    { header: t('audit.colAction'), cell: (a) => actionLabel(a.action) },
    { header: t('audit.colTargetType'), cell: (a) => a.targetType },
    { header: t('audit.colTargetRef'), className: 'font-mono', cell: (a) => a.targetRef },
    {
      header: t('audit.colResult'),
      cell: (a) => (a.result === 'fail' ? <Badge variant="destructive">fail</Badge> : 'ok'),
    },
    { header: t('audit.colClientIp'), cell: (a) => a.clientIp || '-' },
    {
      header: t('audit.colDetail'),
      cell: (a) => (
        <Button type="button" variant="ghost" size="sm" onClick={() => setSelectedAudit(a)}>
          {t('common.view')}
        </Button>
      ),
    },
  ]

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">{t('audit.title')}</h1>
        {/* 导出按钮：按当前已生效过滤条件全量下载（FR-84） */}
        <div className="flex gap-2">
          <Button type="button" variant="outline" size="sm" disabled={exporting} onClick={() => onExport('csv')}>
            {t('audit.exportCsv')}
          </Button>
          <Button type="button" variant="outline" size="sm" disabled={exporting} onClick={() => onExport('json')}>
            {t('audit.exportJson')}
          </Button>
        </div>
      </div>

      <Card>
        <CardContent>
          <form onSubmit={onSearch} className="flex flex-wrap items-end gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="a-namespace">{t('common.namespace')}</Label>
              {/* 筛选框：可编辑下拉，候选来自 listNamespaces 但允许键入列表外值（FR-51） */}
              <Combobox
                id="a-namespace"
                aria-label={t('common.namespace')}
                className="w-40"
                value={namespace}
                onChange={setNamespace}
                options={nsOptions}
                allowCustom
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="a-operator">{t('common.operator')}</Label>
              <Input
                id="a-operator"
                value={operator}
                onChange={(e) => setOperator(e.target.value)}
                placeholder={t('audit.operatorPlaceholder')}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="a-action">{t('audit.colAction')}</Label>
              <Input
                id="a-action"
                value={action}
                onChange={(e) => setAction(e.target.value)}
                placeholder={t('audit.actionPlaceholder')}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="a-targettype">{t('audit.colTargetType')}</Label>
              <Input
                id="a-targettype"
                value={targetType}
                onChange={(e) => setTargetType(e.target.value)}
                placeholder={t('audit.targetTypePlaceholder')}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="a-targetref">{t('audit.colTargetRef')}</Label>
              <Input id="a-targetref" value={targetRef} onChange={(e) => setTargetRef(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="a-detailkw">{t('audit.detailKeyword')}</Label>
              <Input
                id="a-detailkw"
                value={detailKeyword}
                onChange={(e) => setDetailKeyword(e.target.value)}
                placeholder={t('audit.detailKeywordPlaceholder')}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="a-from">{t('audit.fromTime')}</Label>
              <Input
                id="a-from"
                type="datetime-local"
                value={from}
                onChange={(e) => setFrom(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="a-to">{t('audit.toTime')}</Label>
              <Input id="a-to" type="datetime-local" value={to} onChange={(e) => setTo(e.target.value)} />
            </div>
            <Button type="submit">{t('common.query')}</Button>
          </form>
        </CardContent>
      </Card>

      <Card>
        <CardContent>
          <AsyncSection
            isLoading={isLoading}
            isError={isError}
            error={error}
            skeleton={<TableSkeleton columns={columns.length} />}
          >
            <DataTable
              columns={columns}
              rows={data?.items}
              rowKey={(a, idx) => `${a.createdAt}-${idx}`}
              emptyText={t('audit.empty')}
            />

            <div className="mt-4 flex items-center justify-center gap-4 text-sm">
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={page <= 1 || isFetching}
                onClick={() => goPage(page - 1)}
              >
                {t('common.prevPage')}
              </Button>
              <span className="text-muted-foreground">
                {t('common.pageInfo', { page, total: totalPages, count: total })}
              </span>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={page >= totalPages || isFetching}
                onClick={() => goPage(page + 1)}
              >
                {t('common.nextPage')}
              </Button>
            </div>
          </AsyncSection>
        </CardContent>
      </Card>

      {/* 审计详情 Dialog：展示完整 detail 与上下文字段 */}
      <Dialog open={selectedAudit !== null} onOpenChange={(open) => !open && setSelectedAudit(null)}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{t('audit.detailTitle')}</DialogTitle>
          </DialogHeader>
          {selectedAudit && (
            <div className="space-y-4 text-sm">
              <dl className="grid grid-cols-2 gap-x-4 gap-y-2">
                <div>
                  <dt className="text-muted-foreground">{t('audit.colTime')}</dt>
                  <dd>{formatTime(selectedAudit.createdAt)}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">{t('audit.colNamespace')}</dt>
                  <dd>{selectedAudit.namespace || '-'}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">{t('audit.colOperator')}</dt>
                  <dd>{selectedAudit.operator}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">{t('audit.colAction')}</dt>
                  <dd>{actionLabel(selectedAudit.action)}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">{t('audit.colTargetType')}</dt>
                  <dd>{selectedAudit.targetType}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">{t('audit.colTargetRef')}</dt>
                  <dd className="font-mono break-all">{selectedAudit.targetRef}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">{t('audit.colResult')}</dt>
                  <dd>
                    {selectedAudit.result === 'fail' ? (
                      <Badge variant="destructive">fail</Badge>
                    ) : (
                      'ok'
                    )}
                  </dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">{t('audit.colClientIp')}</dt>
                  <dd>{selectedAudit.clientIp || '-'}</dd>
                </div>
              </dl>
              <div className="space-y-1.5">
                <div className="text-muted-foreground">{t('audit.detailField')}</div>
                <pre className="max-h-80 overflow-auto rounded-md border bg-muted p-3 font-mono text-xs whitespace-pre-wrap break-all">
                  {selectedAudit.detail || '-'}
                </pre>
              </div>
            </div>
          )}
        </DialogContent>
      </Dialog>
    </div>
  )
}
