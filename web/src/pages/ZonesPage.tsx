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
import { formatTime } from '../api/format'
import { useMessage } from '../components/useMessage'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
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
          {assignments.isError && (
            <p className="text-sm text-destructive">加载失败：{(assignments.error as Error).message}</p>
          )}
          {assignments.isLoading ? (
            <p className="text-sm text-muted-foreground">加载中…</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>环境</TableHead>
                  <TableHead>serverId</TableHead>
                  <TableHead>大区</TableHead>
                  <TableHead>小区</TableHead>
                  <TableHead>备注</TableHead>
                  <TableHead>更新时间</TableHead>
                  <TableHead>操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {assignments.data && assignments.data.length > 0 ? (
                  assignments.data.map((a) => (
                    <TableRow key={`${a.namespace}/${a.serverId}`}>
                      <TableCell>{a.namespace}</TableCell>
                      <TableCell className="font-mono">{a.serverId}</TableCell>
                      <TableCell>{a.group}</TableCell>
                      <TableCell>{a.zone}</TableCell>
                      <TableCell>{a.note || '-'}</TableCell>
                      <TableCell>{formatTime(a.updatedAt)}</TableCell>
                      <TableCell>
                        <AlertDialog>
                          <AlertDialogTrigger asChild>
                            <Button variant="destructive" size="sm" disabled={unassignMut.isPending}>
                              取消指派
                            </Button>
                          </AlertDialogTrigger>
                          <AlertDialogContent>
                            <AlertDialogHeader>
                              <AlertDialogTitle>确认取消 {a.serverId} 的 zone 指派？</AlertDialogTitle>
                              <AlertDialogDescription>
                                取消后该实例将回到未分配状态。
                              </AlertDialogDescription>
                            </AlertDialogHeader>
                            <AlertDialogFooter>
                              <AlertDialogCancel>取消</AlertDialogCancel>
                              <AlertDialogAction
                                onClick={() =>
                                  unassignMut.mutate({ namespace: a.namespace, serverId: a.serverId })
                                }
                              >
                                确认取消指派
                              </AlertDialogAction>
                            </AlertDialogFooter>
                          </AlertDialogContent>
                        </AlertDialog>
                      </TableCell>
                    </TableRow>
                  ))
                ) : (
                  <TableRow>
                    <TableCell colSpan={7} className="text-center text-muted-foreground">
                      无指派记录
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardContent className="space-y-3">
          <h2 className="text-base font-medium">zone 汇总</h2>
          {summary.isError && (
            <p className="text-sm text-destructive">加载失败：{(summary.error as Error).message}</p>
          )}
          {summary.isLoading ? (
            <p className="text-sm text-muted-foreground">加载中…</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>大区</TableHead>
                  <TableHead>小区</TableHead>
                  <TableHead>服数</TableHead>
                  <TableHead>在线数</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {summary.data && summary.data.length > 0 ? (
                  summary.data.map((s) => (
                    <TableRow key={`${s.group}/${s.zone}`}>
                      <TableCell>{s.group}</TableCell>
                      <TableCell>{s.zone}</TableCell>
                      <TableCell>{s.serverCount}</TableCell>
                      <TableCell>{s.onlineCount}</TableCell>
                    </TableRow>
                  ))
                ) : (
                  <TableRow>
                    <TableCell colSpan={4} className="text-center text-muted-foreground">
                      无汇总数据
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
