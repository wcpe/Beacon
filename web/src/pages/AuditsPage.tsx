// 审计日志页：按 namespace/action/targetType/targetRef/时间范围过滤，分页展示（时间倒序）。

import { useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { listAudits, listNamespaces } from '../api/client'
import type { AuditFilter } from '../api/client'
import type { AuditView } from '../api/types'
import { formatTime } from '../api/format'
import AsyncSection from '@/components/AsyncSection'
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
  // 过滤表单的草稿值（点「查询」时才生效）
  const [namespace, setNamespace] = useState('')
  const [operator, setOperator] = useState('')
  const [action, setAction] = useState('')
  const [targetType, setTargetType] = useState('')
  const [targetRef, setTargetRef] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  // 已生效的查询条件
  const [filter, setFilter] = useState<AuditFilter>({ page: 1, size: PAGE_SIZE })
  // 详情 Dialog 选中的审计条目（null 表示关闭）
  const [selectedAudit, setSelectedAudit] = useState<AuditView | null>(null)

  const { data, isLoading, isError, error, isFetching } = useQuery({
    queryKey: ['audits', filter],
    queryFn: () => listAudits(filter),
    placeholderData: keepPreviousData,
  })

  // 环境筛选下拉的候选来源（FR-51）：来自 listNamespaces，筛选框允许键入候选外的值（可编辑）
  const namespacesQuery = useQuery({ queryKey: ['namespaces'], queryFn: () => listNamespaces() })
  const namespaceOptions = (namespacesQuery.data ?? []).map((n) => n.code)

  function onSearch(e: React.FormEvent) {
    e.preventDefault()
    setFilter({
      namespace: namespace.trim() || undefined,
      operator: operator.trim() || undefined,
      action: action.trim() || undefined,
      targetType: targetType.trim() || undefined,
      targetRef: targetRef.trim() || undefined,
      from: toIso(from),
      to: toIso(to),
      page: 1,
      size: PAGE_SIZE,
    })
  }

  function goPage(page: number) {
    setFilter((f) => ({ ...f, page }))
  }

  const total = data?.total ?? 0
  const page = filter.page ?? 1
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  // 审计表列定义（详情列闭包引用 setSelectedAudit，故在组件内定义）
  const columns: DataTableColumn<AuditView>[] = [
    { header: '时间', cell: (a) => formatTime(a.createdAt) },
    { header: '环境', cell: (a) => a.namespace || '-' },
    { header: '操作人', cell: (a) => a.operator },
    { header: '动作', cell: (a) => a.action },
    { header: '对象类型', cell: (a) => a.targetType },
    { header: '对象定位', className: 'font-mono', cell: (a) => a.targetRef },
    {
      header: '结果',
      cell: (a) => (a.result === 'fail' ? <Badge variant="destructive">fail</Badge> : 'ok'),
    },
    { header: '来源 IP', cell: (a) => a.clientIp || '-' },
    {
      header: '详情',
      cell: (a) => (
        <Button type="button" variant="ghost" size="sm" onClick={() => setSelectedAudit(a)}>
          查看
        </Button>
      ),
    },
  ]

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">审计日志</h1>
      </div>

      <Card>
        <CardContent>
          <form onSubmit={onSearch} className="flex flex-wrap items-end gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="a-namespace">环境</Label>
              {/* 筛选框：可编辑下拉，候选来自 listNamespaces 但允许键入列表外值（FR-51） */}
              <Combobox
                id="a-namespace"
                aria-label="环境"
                className="w-40"
                value={namespace}
                onChange={setNamespace}
                options={namespaceOptions}
                allowCustom
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="a-operator">操作人</Label>
              <Input
                id="a-operator"
                value={operator}
                onChange={(e) => setOperator(e.target.value)}
                placeholder="如 admin"
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="a-action">动作</Label>
              <Input
                id="a-action"
                value={action}
                onChange={(e) => setAction(e.target.value)}
                placeholder="如 config.publish"
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="a-targettype">对象类型</Label>
              <Input
                id="a-targettype"
                value={targetType}
                onChange={(e) => setTargetType(e.target.value)}
                placeholder="config / zone / ..."
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="a-targetref">对象定位</Label>
              <Input id="a-targetref" value={targetRef} onChange={(e) => setTargetRef(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="a-from">起始时间</Label>
              <Input
                id="a-from"
                type="datetime-local"
                value={from}
                onChange={(e) => setFrom(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="a-to">截止时间</Label>
              <Input id="a-to" type="datetime-local" value={to} onChange={(e) => setTo(e.target.value)} />
            </div>
            <Button type="submit">查询</Button>
          </form>
        </CardContent>
      </Card>

      <Card>
        <CardContent>
          <AsyncSection isLoading={isLoading} isError={isError} error={error}>
            <DataTable
              columns={columns}
              rows={data?.items}
              rowKey={(a, idx) => `${a.createdAt}-${idx}`}
              emptyText="无审计记录"
            />

            <div className="mt-4 flex items-center justify-center gap-4 text-sm">
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={page <= 1 || isFetching}
                onClick={() => goPage(page - 1)}
              >
                上一页
              </Button>
              <span className="text-muted-foreground">
                第 {page} / {totalPages} 页（共 {total} 条）
              </span>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={page >= totalPages || isFetching}
                onClick={() => goPage(page + 1)}
              >
                下一页
              </Button>
            </div>
          </AsyncSection>
        </CardContent>
      </Card>

      {/* 审计详情 Dialog：展示完整 detail 与上下文字段 */}
      <Dialog open={selectedAudit !== null} onOpenChange={(open) => !open && setSelectedAudit(null)}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>审计详情</DialogTitle>
          </DialogHeader>
          {selectedAudit && (
            <div className="space-y-4 text-sm">
              <dl className="grid grid-cols-2 gap-x-4 gap-y-2">
                <div>
                  <dt className="text-muted-foreground">时间</dt>
                  <dd>{formatTime(selectedAudit.createdAt)}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">环境</dt>
                  <dd>{selectedAudit.namespace || '-'}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">操作人</dt>
                  <dd>{selectedAudit.operator}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">动作</dt>
                  <dd>{selectedAudit.action}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">对象类型</dt>
                  <dd>{selectedAudit.targetType}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">对象定位</dt>
                  <dd className="font-mono break-all">{selectedAudit.targetRef}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">结果</dt>
                  <dd>
                    {selectedAudit.result === 'fail' ? (
                      <Badge variant="destructive">fail</Badge>
                    ) : (
                      'ok'
                    )}
                  </dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">来源 IP</dt>
                  <dd>{selectedAudit.clientIp || '-'}</dd>
                </div>
              </dl>
              <div className="space-y-1.5">
                <div className="text-muted-foreground">详情</div>
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
