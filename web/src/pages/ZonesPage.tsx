// zone 分配页：指派列表 + 新增/改派（Dialog）+ 取消指派 + zone 维度汇总。

import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  assignZone,
  listAssignments,
  unassignZone,
  zoneSummary,
} from '../api/client'
import type { AssignParams } from '../api/client'
import type { AssignmentView, ZoneStatView } from '../api/types'
import { formatTime } from '../api/format'
import { useMessage } from '../components/useMessage'
import AsyncSection from '@/components/AsyncSection'
import DataTable, { type DataTableColumn } from '@/components/DataTable'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog'

// 指派/汇总共用的过滤条件
interface ZoneFilter {
  namespace?: string
  group?: string
  zone?: string
}

// 新增/改派表单初值
const EMPTY_FORM = { namespace: '', serverId: '', group: '', zone: '', note: '' }

// zone 汇总列定义（无副作用，模块级）
const SUMMARY_COLUMNS: DataTableColumn<ZoneStatView>[] = [
  { header: '大区', cell: (s) => s.group },
  { header: '小区', cell: (s) => s.zone },
  { header: '服数', cell: (s) => s.serverCount },
  { header: '在线数', cell: (s) => s.onlineCount },
]

export default function ZonesPage() {
  const qc = useQueryClient()
  const msg = useMessage()

  // 过滤草稿与生效值
  const [fNamespace, setFNamespace] = useState('')
  const [fGroup, setFGroup] = useState('')
  const [fZone, setFZone] = useState('')
  const [filter, setFilter] = useState<ZoneFilter>({})

  // 新增/改派表单与 Dialog 开关
  const [form, setForm] = useState(EMPTY_FORM)
  const [assignOpen, setAssignOpen] = useState(false)

  const assignments = useQuery({
    queryKey: ['assignments', filter],
    queryFn: () => listAssignments(filter.namespace, filter.group, filter.zone),
  })

  const summary = useQuery({
    queryKey: ['zone-summary', filter.namespace, filter.group],
    queryFn: () => zoneSummary(filter.namespace, filter.group),
  })

  const assignMut = useMutation({
    mutationFn: (params: AssignParams) => assignZone(params),
    onSuccess: (a) => {
      msg.showSuccess(`已指派 ${a.serverId} → ${a.zone}`)
      setForm(EMPTY_FORM)
      setAssignOpen(false)
      invalidate()
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  const unassignMut = useMutation({
    mutationFn: (vars: { namespace: string; serverId: string }) =>
      unassignZone(vars.namespace, vars.serverId),
    onSuccess: (_d, vars) => {
      msg.showSuccess(`已取消 ${vars.serverId} 的指派`)
      invalidate()
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function invalidate() {
    qc.invalidateQueries({ queryKey: ['assignments'] })
    qc.invalidateQueries({ queryKey: ['zone-summary'] })
  }

  function onSearch(e: React.FormEvent) {
    e.preventDefault()
    setFilter({
      namespace: fNamespace.trim() || undefined,
      group: fGroup.trim() || undefined,
      zone: fZone.trim() || undefined,
    })
  }

  function onAssign(e: React.FormEvent) {
    e.preventDefault()
    if (!form.namespace.trim() || !form.serverId.trim() || !form.group.trim() || !form.zone.trim()) {
      msg.showError('环境、serverId、大区、小区均为必填')
      return
    }
    assignMut.mutate({
      namespace: form.namespace.trim(),
      serverId: form.serverId.trim(),
      group: form.group.trim(),
      zone: form.zone.trim(),
      note: form.note.trim(),
    })
  }

  // 指派列表列定义（操作列闭包引用 unassignMut，故在组件内定义）
  const assignmentColumns: DataTableColumn<AssignmentView>[] = [
    { header: '环境', cell: (a) => a.namespace },
    { header: 'serverId', className: 'font-mono', cell: (a) => a.serverId },
    { header: '大区', cell: (a) => a.group },
    { header: '小区', cell: (a) => a.zone },
    { header: '备注', cell: (a) => a.note || '-' },
    { header: '更新时间', cell: (a) => formatTime(a.updatedAt) },
    {
      header: '操作',
      cell: (a) => (
        <AlertDialog>
          <AlertDialogTrigger asChild>
            <Button variant="destructive" size="sm" disabled={unassignMut.isPending}>
              取消指派
            </Button>
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>确认取消 {a.serverId} 的 zone 指派？</AlertDialogTitle>
              <AlertDialogDescription>取消后该实例将回到未分配状态。</AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>取消</AlertDialogCancel>
              <AlertDialogAction
                onClick={() => unassignMut.mutate({ namespace: a.namespace, serverId: a.serverId })}
              >
                确认取消指派
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      ),
    },
  ]

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">zone 分配</h1>
        <Dialog open={assignOpen} onOpenChange={setAssignOpen}>
          <DialogTrigger asChild>
            <Button>新增 / 改派</Button>
          </DialogTrigger>
          <DialogContent className="sm:max-w-2xl">
            <DialogHeader>
              <DialogTitle>新增 / 改派</DialogTitle>
            </DialogHeader>
            <form id="assign-zone" onSubmit={onAssign} className="grid grid-cols-2 gap-4">
              <div className="space-y-1.5">
                <Label htmlFor="a-namespace">环境</Label>
                <Input
                  id="a-namespace"
                  value={form.namespace}
                  onChange={(e) => setForm({ ...form, namespace: e.target.value })}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="a-serverid">serverId</Label>
                <Input
                  id="a-serverid"
                  value={form.serverId}
                  onChange={(e) => setForm({ ...form, serverId: e.target.value })}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="a-group">大区</Label>
                <Input
                  id="a-group"
                  value={form.group}
                  onChange={(e) => setForm({ ...form, group: e.target.value })}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="a-zone">小区</Label>
                <Input
                  id="a-zone"
                  value={form.zone}
                  onChange={(e) => setForm({ ...form, zone: e.target.value })}
                />
              </div>
              <div className="col-span-2 space-y-1.5">
                <Label htmlFor="a-note">备注</Label>
                <Input
                  id="a-note"
                  value={form.note}
                  onChange={(e) => setForm({ ...form, note: e.target.value })}
                />
              </div>
            </form>
            <DialogFooter>
              <Button type="submit" form="assign-zone" disabled={assignMut.isPending}>
                指派
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </div>

      <Card>
        <CardContent>
          <form onSubmit={onSearch} className="flex flex-wrap items-end gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="f-namespace">环境</Label>
              <Input id="f-namespace" value={fNamespace} onChange={(e) => setFNamespace(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="f-group">大区</Label>
              <Input id="f-group" value={fGroup} onChange={(e) => setFGroup(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="f-zone">小区</Label>
              <Input id="f-zone" value={fZone} onChange={(e) => setFZone(e.target.value)} />
            </div>
            <Button type="submit">查询</Button>
          </form>
        </CardContent>
      </Card>

      <Card>
        <CardContent className="space-y-3">
          <h2 className="text-base font-medium">指派列表</h2>
          <AsyncSection
            isLoading={assignments.isLoading}
            isError={assignments.isError}
            error={assignments.error}
          >
            <DataTable
              columns={assignmentColumns}
              rows={assignments.data}
              rowKey={(a) => `${a.namespace}/${a.serverId}`}
              emptyText="无指派记录"
            />
          </AsyncSection>
        </CardContent>
      </Card>

      <Card>
        <CardContent className="space-y-3">
          <h2 className="text-base font-medium">zone 汇总</h2>
          <AsyncSection isLoading={summary.isLoading} isError={summary.isError} error={summary.error}>
            <DataTable
              columns={SUMMARY_COLUMNS}
              rows={summary.data}
              rowKey={(s) => `${s.group}/${s.zone}`}
              emptyText="无汇总数据"
            />
          </AsyncSection>
        </CardContent>
      </Card>
    </div>
  )
}
