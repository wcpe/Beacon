// zone 分配页（看板式归派，FR-35）：
// 左侧未指派 server 卡片池 + 右侧按大区(group)分组的 zone 容器（放置桶）。
// 拖卡进某 zone = 指派、跨桶拖 = 改派、拖回未指派 = 取消指派；复用既有 API、后端零改动（增强 FR-8）。
// 保留「新增 zone / 指派」表单入口（用于建空 zone 的首次指派）+ zone 维度汇总。

import { useMemo, useState } from 'react'
import {
  DndContext,
  DragOverlay,
  PointerSensor,
  useSensor,
  useSensors,
  type DragEndEvent,
  type DragStartEvent,
} from '@dnd-kit/core'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  assignZone,
  listAssignments,
  listInstances,
  unassignZone,
  zoneSummary,
} from '../api/client'
import type { AssignParams } from '../api/client'
import type { InstanceView, ZoneStatView } from '../api/types'
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
  buildKanbanModel,
  noteForServer,
  type ZoneBucket,
} from './zones/kanbanModel'
import {
  encodeZoneDroppableId,
  resolveDragAction,
  UNASSIGNED_DROPPABLE_ID,
} from './zones/dragAction'
import ServerCard from './zones/ServerCard'
import DropBucket from './zones/DropBucket'

// 指派/汇总共用的过滤条件
interface ZoneFilter {
  namespace?: string
  group?: string
  zone?: string
}

// 新增 zone / 指派表单初值
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

  // 新增 zone / 指派表单与 Dialog 开关
  const [form, setForm] = useState(EMPTY_FORM)
  const [assignOpen, setAssignOpen] = useState(false)

  // 拖拽中卡片对应实例（用于 DragOverlay 叠层预览；null 表示未拖动）
  const [dragging, setDragging] = useState<InstanceView | null>(null)

  // 指针拖拽前需移动 5px 才激活，避免与卡片点击/误触冲突
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 5 } }))

  const instances = useQuery({
    queryKey: ['instances', 'zone-kanban', filter],
    queryFn: () => listInstances({ namespace: filter.namespace, group: filter.group, zone: filter.zone }),
  })

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
    qc.invalidateQueries({ queryKey: ['instances'] })
    qc.invalidateQueries({ queryKey: ['assignments'] })
    qc.invalidateQueries({ queryKey: ['zone-summary'] })
  }

  // 由三个查询结果派生看板模型（纯函数，结果稳定排序）
  const model = useMemo(
    () => buildKanbanModel(instances.data ?? [], summary.data ?? []),
    [instances.data, summary.data],
  )

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

  function onDragStart(e: DragStartEvent) {
    const inst = e.active.data.current?.instance as InstanceView | undefined
    setDragging(inst ?? null)
  }

  // 拖拽结束：按落点解析为指派/改派/取消指派，再调既有 API（逻辑见 dragAction.resolveDragAction）
  function onDragEnd(e: DragEndEvent) {
    const inst = e.active.data.current?.instance as InstanceView | undefined
    setDragging(null)
    if (!inst) return
    const action = resolveDragAction(inst, e.over ? String(e.over.id) : null)
    if (action.kind === 'assign') {
      // 改派沿用现有备注，避免拖拽清空运维填写的备注
      const note = noteForServer(assignments.data ?? [], inst.namespace, inst.serverId)
      assignMut.mutate({ ...action.params, note })
    } else if (action.kind === 'unassign') {
      unassignMut.mutate({ namespace: action.namespace, serverId: action.serverId })
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">zone 分配</h1>
        <Dialog open={assignOpen} onOpenChange={setAssignOpen}>
          <DialogTrigger asChild>
            <Button>新增 zone / 指派</Button>
          </DialogTrigger>
          <DialogContent className="sm:max-w-2xl">
            <DialogHeader>
              <DialogTitle>新增 zone / 指派</DialogTitle>
            </DialogHeader>
            <p className="text-sm text-muted-foreground">
              指派首个 server 即创建该 zone；已有 zone 也可直接拖拽卡片归派。
            </p>
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
          <div className="flex items-baseline justify-between">
            <h2 className="text-base font-medium">归派看板</h2>
            <p className="text-sm text-muted-foreground">
              拖卡到 zone = 指派 / 改派；拖回未指派池 = 取消指派
            </p>
          </div>
          <AsyncSection
            isLoading={instances.isLoading || summary.isLoading}
            isError={instances.isError || summary.isError}
            error={instances.error ?? summary.error}
          >
            <DndContext
              sensors={sensors}
              onDragStart={onDragStart}
              onDragEnd={onDragEnd}
            >
              <div className="grid grid-cols-1 gap-4 lg:grid-cols-[18rem_1fr]">
                {/* 左侧未指派池 */}
                <DropBucket
                  id={UNASSIGNED_DROPPABLE_ID}
                  title="未指派"
                  meta={`${model.unassigned.length} 台`}
                >
                  {model.unassigned.length === 0 ? (
                    <p className="px-0.5 py-2 text-xs text-muted-foreground">无未指派实例</p>
                  ) : (
                    model.unassigned.map((i) => (
                      <ServerCard key={`${i.namespace}/${i.serverId}`} instance={i} />
                    ))
                  )}
                </DropBucket>

                {/* 右侧按大区分组的 zone 桶 */}
                <div className="space-y-4">
                  {model.groups.length === 0 ? (
                    <p className="text-sm text-muted-foreground">
                      暂无 zone，请用「新增 zone / 指派」创建第一个 zone。
                    </p>
                  ) : (
                    model.groups.map((col) => (
                      <div key={col.group} className="space-y-2">
                        <div className="text-sm font-semibold">大区 {col.group}</div>
                        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
                          {col.zones.map((bucket) => (
                            <ZoneDropBucket key={`${bucket.group}/${bucket.zone}`} bucket={bucket} />
                          ))}
                        </div>
                      </div>
                    ))
                  )}
                </div>
              </div>

              {/* 拖拽叠层：跟随指针的卡片预览 */}
              <DragOverlay>
                {dragging ? <ServerCard instance={dragging} /> : null}
              </DragOverlay>
            </DndContext>
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

// 单个 zone 放置桶：标题为小区名 + 实例数，内含其卡片
function ZoneDropBucket({ bucket }: { bucket: ZoneBucket }) {
  return (
    <DropBucket
      id={encodeZoneDroppableId(bucket.group, bucket.zone)}
      title={bucket.zone}
      meta={`${bucket.instances.length} 台`}
    >
      {bucket.instances.length === 0 ? (
        <p className="px-0.5 py-2 text-xs text-muted-foreground">拖卡到此指派</p>
      ) : (
        bucket.instances.map((i) => (
          <ServerCard key={`${i.namespace}/${i.serverId}`} instance={i} />
        ))
      )}
    </DropBucket>
  )
}
