// zone 分配页（看板式归派，FR-35）：
// 左侧未指派 server 卡片池 + 右侧按大区(group)分组的 zone 容器（放置桶）。
// 拖卡进某 zone = 指派、跨桶拖 = 改派、拖回未指派 = 取消指派；复用既有 API、后端零改动（增强 FR-8）。
// 保留「新增 zone / 指派」表单入口（用于建空 zone 的首次指派）+ zone 维度汇总。
// 指派表单的环境 / serverId / 大区 / 小区改为从 API 拉取的下拉（serverId 仅列 bukkit 子服）并加非法值校验（增强 FR-40）；备注仍为自由文本。

import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
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
  listNamespaces,
  unassignZone,
  zoneSummary,
} from '../api/client'
import type { AssignParams } from '../api/client'
import type { InstanceView } from '../api/types'
import { useMessage } from '../components/useMessage'
import AsyncSection from '@/components/AsyncSection'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'
import { Combobox } from '@/components/ui/combobox'
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
import { buildSummaryTree } from './zones/summaryTree'
import {
  encodeZoneDroppableId,
  resolveDragAction,
  UNASSIGNED_DROPPABLE_ID,
} from './zones/dragAction'
import ServerCard from './zones/ServerCard'
import DropBucket from './zones/DropBucket'
import ZoneSummaryTree from './zones/ZoneSummaryTree'

// 指派/汇总共用的过滤条件
interface ZoneFilter {
  namespace?: string
  group?: string
  zone?: string
}

// 新增 zone / 指派表单初值
const EMPTY_FORM = { namespace: '', serverId: '', group: '', zone: '', note: '' }

export default function ZonesPage() {
  const { t } = useTranslation()
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

  // 指派表单下拉的选项来源（FR-40 增强）：环境 / 实例 / zone 汇总均不随搜索过滤，
  // 全量拉取以免表单候选被看板过滤条件意外收窄。
  const namespacesQuery = useQuery({ queryKey: ['namespaces'], queryFn: () => listNamespaces() })
  const allInstances = useQuery({
    queryKey: ['instances', 'zone-form-options'],
    queryFn: () => listInstances({}),
  })
  const allSummary = useQuery({ queryKey: ['zone-summary', 'all'], queryFn: () => zoneSummary() })

  // 环境候选：来自 listNamespaces
  const namespaceOptions = useMemo(
    () => (namespacesQuery.data ?? []).map((n) => n.code),
    [namespacesQuery.data],
  )
  // 大区候选：zone 汇总与实例列表去重并集（兼容无 zone 指派但已注册的大区）
  const groupOptions = useMemo(() => {
    const set = new Set<string>()
    for (const z of allSummary.data ?? []) if (z.group) set.add(z.group)
    for (const i of allInstances.data ?? []) if (i.group) set.add(i.group)
    return Array.from(set).sort()
  }, [allSummary.data, allInstances.data])
  // 小区候选：zone 汇总与实例列表去重并集
  const zoneOptions = useMemo(() => {
    const set = new Set<string>()
    for (const z of allSummary.data ?? []) if (z.zone) set.add(z.zone)
    for (const i of allInstances.data ?? []) if (i.zone) set.add(i.zone)
    return Array.from(set).sort()
  }, [allSummary.data, allInstances.data])
  // serverId 候选：仅 bukkit 子服（BC 代理不可被指派进 zone，与后端校验一致，FR-8/FR-35）
  const serverOptions = useMemo(
    () =>
      (allInstances.data ?? [])
        .filter((i) => i.role === 'bukkit')
        .map((i) => i.serverId)
        .sort(),
    [allInstances.data],
  )

  const assignMut = useMutation({
    mutationFn: (params: AssignParams) => assignZone(params),
    onSuccess: (a) => {
      msg.showSuccess(t('zones.msgAssigned', { serverId: a.serverId, zone: a.zone }))
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
      msg.showSuccess(t('zones.msgUnassigned', { serverId: vars.serverId }))
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

  // 由 summary + 看板模型派生汇总树（大区→小区→子服；计数取自 summary，与原表口径一致，FR-55）
  const summaryTreeModel = useMemo(
    () => buildSummaryTree(summary.data ?? [], model),
    [summary.data, model],
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
    if (!form.namespace || !form.serverId || !form.group || !form.zone) {
      msg.showError(t('zones.requiredFields'))
      return
    }
    // 非法值拦截：所选项须落在 API 拉来的候选内（防手改 DOM 或脏缓存提交越界值）
    if (
      !namespaceOptions.includes(form.namespace) ||
      !serverOptions.includes(form.serverId) ||
      !groupOptions.includes(form.group) ||
      !zoneOptions.includes(form.zone)
    ) {
      msg.showError(t('zones.invalidValues'))
      return
    }
    assignMut.mutate({
      namespace: form.namespace,
      serverId: form.serverId,
      group: form.group,
      zone: form.zone,
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
        <h1 className="text-xl font-semibold">{t('zones.title')}</h1>
        <Dialog open={assignOpen} onOpenChange={setAssignOpen}>
          <DialogTrigger asChild>
            <Button>{t('zones.addAssign')}</Button>
          </DialogTrigger>
          <DialogContent className="sm:max-w-2xl">
            <DialogHeader>
              <DialogTitle>{t('zones.addAssign')}</DialogTitle>
            </DialogHeader>
            <p className="text-sm text-muted-foreground">
              {t('zones.assignDialogDesc')}
            </p>
            <form id="assign-zone" onSubmit={onAssign} className="grid grid-cols-2 gap-4">
              <div className="space-y-1.5">
                <Label htmlFor="a-namespace">{t('common.namespace')}</Label>
                {/* 严格选：指派目标须为已存在维度（不接受列表外值，FR-51 增强 FR-40） */}
                <Combobox
                  id="a-namespace"
                  aria-label={t('common.namespace')}
                  value={form.namespace}
                  onChange={(v) => setForm({ ...form, namespace: v })}
                  options={namespaceOptions}
                  allowCustom={false}
                  placeholder={t('common.pleaseSelect')}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="a-serverid">serverId</Label>
                {/* 仅列 bukkit 子服：BC 代理不可被指派进 zone（与后端校验一致，FR-8/FR-35） */}
                <Combobox
                  id="a-serverid"
                  aria-label="serverId"
                  value={form.serverId}
                  onChange={(v) => setForm({ ...form, serverId: v })}
                  options={serverOptions}
                  allowCustom={false}
                  placeholder={t('common.pleaseSelect')}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="a-group">{t('common.group')}</Label>
                <Combobox
                  id="a-group"
                  aria-label={t('common.group')}
                  value={form.group}
                  onChange={(v) => setForm({ ...form, group: v })}
                  options={groupOptions}
                  allowCustom={false}
                  placeholder={t('common.pleaseSelect')}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="a-zone">{t('common.zone')}</Label>
                <Combobox
                  id="a-zone"
                  aria-label={t('common.zone')}
                  value={form.zone}
                  onChange={(v) => setForm({ ...form, zone: v })}
                  options={zoneOptions}
                  allowCustom={false}
                  placeholder={t('common.pleaseSelect')}
                />
              </div>
              <div className="col-span-2 space-y-1.5">
                <Label htmlFor="a-note">{t('zones.formNote')}</Label>
                <Input
                  id="a-note"
                  value={form.note}
                  onChange={(e) => setForm({ ...form, note: e.target.value })}
                />
              </div>
            </form>
            <DialogFooter>
              <Button type="submit" form="assign-zone" disabled={assignMut.isPending}>
                {t('zones.assignBtn')}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </div>

      <Card>
        <CardContent>
          <form onSubmit={onSearch} className="flex flex-wrap items-end gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="f-namespace">{t('common.namespace')}</Label>
              <Input id="f-namespace" value={fNamespace} onChange={(e) => setFNamespace(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="f-group">{t('common.group')}</Label>
              <Input id="f-group" value={fGroup} onChange={(e) => setFGroup(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="f-zone">{t('common.zone')}</Label>
              <Input id="f-zone" value={fZone} onChange={(e) => setFZone(e.target.value)} />
            </div>
            <Button type="submit">{t('common.query')}</Button>
          </form>
        </CardContent>
      </Card>

      {/* zone 汇总树（大区→小区→子服）：上移至看板之上，替代原底部扁平表格（FR-55） */}
      <Card>
        <CardContent className="space-y-3">
          <h2 className="text-base font-medium">{t('zones.summaryTitle')}</h2>
          <AsyncSection
            isLoading={summary.isLoading || instances.isLoading}
            isError={summary.isError || instances.isError}
            error={summary.error ?? instances.error}
          >
            <ZoneSummaryTree tree={summaryTreeModel} />
          </AsyncSection>
        </CardContent>
      </Card>

      <Card>
        <CardContent className="space-y-3">
          <div className="flex items-baseline justify-between">
            <h2 className="text-base font-medium">{t('zones.kanbanTitle')}</h2>
            <p className="text-sm text-muted-foreground">
              {t('zones.kanbanHint')}
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
                  title={t('zones.unassignedTitle')}
                  meta={t('zones.unitServers', { count: model.unassigned.length })}
                >
                  {model.unassigned.length === 0 ? (
                    <p className="px-0.5 py-2 text-xs text-muted-foreground">{t('zones.noUnassigned')}</p>
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
                      {t('zones.noZones')}
                    </p>
                  ) : (
                    model.groups.map((col) => (
                      <div key={col.group} className="space-y-2">
                        <div className="text-sm font-semibold">{t('zones.groupLabel', { group: col.group })}</div>
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
    </div>
  )
}

// 单个 zone 放置桶：标题为小区名 + 实例数，内含其卡片
function ZoneDropBucket({ bucket }: { bucket: ZoneBucket }) {
  const { t } = useTranslation()
  return (
    <DropBucket
      id={encodeZoneDroppableId(bucket.group, bucket.zone)}
      title={bucket.zone}
      meta={t('zones.unitServers', { count: bucket.instances.length })}
    >
      {bucket.instances.length === 0 ? (
        <p className="px-0.5 py-2 text-xs text-muted-foreground">{t('zones.dropHere')}</p>
      ) : (
        bucket.instances.map((i) => (
          <ServerCard key={`${i.namespace}/${i.serverId}`} instance={i} />
        ))
      )}
    </DropBucket>
  )
}
