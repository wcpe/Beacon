// 区分配页（看板式归派，FR-35 + 安全化 FR-71）：
// 左侧未指派 server 卡片池 + 右侧按大区(group)分组的 zone 容器。
// FR-71 取消「拖拽即改」：看板默认只读，须显式「解锁改派」后逐卡走「改派」对话框（手输 serverId 复述）/「取消指派」二次确认；
// 安全由后端排空门兜底（在线非空服改区返 409 ZONE_SERVER_ONLINE_NONEMPTY），前端摩擦只防误触（ADR-0036）。
// 保留「新增 区 / 指派」表单入口（用于建空区的首次指派）+ 区维度汇总。
// 指派表单的环境 / serverId / 大区 / 小区为从 API 拉取的下拉（serverId 仅列 bukkit 子服）并加非法值校验（增强 FR-40/FR-51）；备注仍为自由文本。

import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Filter, LayoutGrid, ListTree } from 'lucide-react'
import {
  ApiClientError,
  assignZone,
  listAssignments,
  listInstances,
  listNamespaces,
  unassignZone,
  zoneSummary,
} from '../api/client'
import type { AssignParams } from '../api/client'
import { namespaceOptions } from '../api/format'
import type { InstanceView } from '../api/types'
import { useMessage } from '../components/useMessage'
import { usePageHeader } from '@/components/PageHeader'
import { useEnvironment } from '@/state/environment'
import AsyncSection from '@/components/AsyncSection'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import SectionHeader from '@/components/SectionHeader'
import { Combobox } from '@/components/ui/combobox'
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
import ServerCard from './zones/ServerCard'
import DropBucket from './zones/DropBucket'
import ReassignDialog from './zones/ReassignDialog'
import ZoneSummaryTree from './zones/ZoneSummaryTree'

// 指派/汇总共用的过滤条件
interface ZoneFilter {
  namespace?: string
  group?: string
  zone?: string
}

// 新增 zone / 指派表单初值
const EMPTY_FORM = { namespace: '', serverId: '', group: '', zone: '', note: '' }

// 排空门错误码（与后端 apperr.ErrZoneServerOnlineNonempty 一致，FR-71/ADR-0036）
const ERR_ZONE_SERVER_ONLINE_NONEMPTY = 'ZONE_SERVER_ONLINE_NONEMPTY'

export default function ZonesPage() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const msg = useMessage()

  // 环境收口（FR-105 真机打磨）：看板/汇总的环境改读页眉全局环境，不再页内自管环境筛选；大区/小区筛选保留页内。
  // 注：下方「新增 区 / 指派」表单的环境字段是写入项（非筛选），仍保留其下拉（候选见 nsOptions）。
  const namespace = useEnvironment()
  // 过滤草稿与生效值（不含 namespace；namespace 由全局环境合并）
  const [fGroup, setFGroup] = useState('')
  const [fZone, setFZone] = useState('')
  const [filter, setFilter] = useState<ZoneFilter>({})

  // 生效过滤 = 页内大区/小区筛选 + 全局环境（空串＝全部环境）。全局环境变化即重算 → 各查询 queryKey 含其 namespace → 自动重查。
  const effectiveFilter = useMemo<ZoneFilter>(
    () => ({ ...filter, namespace: namespace || undefined }),
    [filter, namespace],
  )

  // 新增 zone / 指派表单与 Dialog 开关
  const [form, setForm] = useState(EMPTY_FORM)
  const [assignOpen, setAssignOpen] = useState(false)

  // 看板默认只读（FR-71）：解锁后才出逐卡改派 / 取消指派入口
  const [unlocked, setUnlocked] = useState(false)
  // 当前正在改派的实例（null 表示改派对话框关闭）
  const [reassignTarget, setReassignTarget] = useState<InstanceView | null>(null)

  const instances = useQuery({
    queryKey: ['instances', 'zone-kanban', effectiveFilter],
    queryFn: () =>
      listInstances({
        namespace: effectiveFilter.namespace,
        group: effectiveFilter.group,
        zone: effectiveFilter.zone,
      }),
  })

  const assignments = useQuery({
    queryKey: ['assignments', effectiveFilter],
    queryFn: () => listAssignments(effectiveFilter.namespace, effectiveFilter.group, effectiveFilter.zone),
  })

  const summary = useQuery({
    queryKey: ['zone-summary', effectiveFilter.namespace, effectiveFilter.group],
    queryFn: () => zoneSummary(effectiveFilter.namespace, effectiveFilter.group),
  })

  // 指派表单下拉的选项来源（FR-40 增强）：环境 / 实例 / zone 汇总均不随搜索过滤，
  // 全量拉取以免表单候选被看板过滤条件意外收窄。
  const namespacesQuery = useQuery({ queryKey: ['namespaces'], queryFn: () => listNamespaces() })
  const allInstances = useQuery({
    queryKey: ['instances', 'zone-form-options'],
    queryFn: () => listInstances({}),
  })
  const allSummary = useQuery({ queryKey: ['zone-summary', 'all'], queryFn: () => zoneSummary() })

  // 环境候选：来自 listNamespaces。下拉显示「编码 · 名称」（FR-70）；校验仍用纯 code 集合。
  const nsOptions = useMemo(
    () => namespaceOptions(namespacesQuery.data),
    [namespacesQuery.data],
  )
  const namespaceCodes = useMemo(
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

  // 区分排空门 409 与一般错误：在线非空服改区被硬拒时给「先排空」专属中文提示（FR-71/ADR-0036）
  function reportError(e: unknown) {
    if (e instanceof ApiClientError && e.code === ERR_ZONE_SERVER_ONLINE_NONEMPTY) {
      msg.showError(t('zones.drainGateHint'))
      return
    }
    msg.showError(e instanceof Error ? e.message : t('common.unknownError'))
  }

  const assignMut = useMutation({
    mutationFn: (params: AssignParams) => assignZone(params),
    onSuccess: (a) => {
      msg.showSuccess(t('zones.msgAssigned', { serverId: a.serverId, zone: a.zone }))
      setForm(EMPTY_FORM)
      setAssignOpen(false)
      setReassignTarget(null)
      invalidate()
    },
    onError: reportError,
  })

  const unassignMut = useMutation({
    mutationFn: (vars: { namespace: string; serverId: string }) =>
      unassignZone(vars.namespace, vars.serverId),
    onSuccess: (_d, vars) => {
      msg.showSuccess(t('zones.msgUnassigned', { serverId: vars.serverId }))
      invalidate()
    },
    onError: reportError,
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
    // namespace 不在页内筛选；由全局环境合并进 effectiveFilter。
    setFilter({
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
      !namespaceCodes.includes(form.namespace) ||
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

  // 页眉（FR-105）：标题 + 新增 区 / 指派对话框整体移入主操作槽（assignOpen 受控状态仍在本组件）
  usePageHeader({
    title: t('zones.title'),
    envScoped: true,
    actions: (
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
                options={nsOptions}
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
    ),
  })

  return (
    <div className="space-y-6">
      {/* 筛选条（FR-107 卡片降级）：区段标题 + 细线轻分隔，替代原筛选 Card 外壳 */}
      <section className="space-y-3">
        <SectionHeader icon={<Filter className="size-4" />} title={t('common.filter')} />
        <form onSubmit={onSearch} className="flex flex-wrap items-end gap-3">
          {/* 环境收口（FR-105 真机打磨）：原页内环境筛选已移除，看板/汇总环境改读页眉全局环境槽。 */}
          <div className="space-y-1.5">
            <Label htmlFor="f-group">{t('common.group')}</Label>
            <Combobox
              id="f-group"
              aria-label={t('common.group')}
              className="w-40"
              value={fGroup}
              onChange={setFGroup}
              options={groupOptions}
              allowCustom
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="f-zone">{t('common.zone')}</Label>
            <Combobox
              id="f-zone"
              aria-label={t('common.zone')}
              className="w-40"
              value={fZone}
              onChange={setFZone}
              options={zoneOptions}
              allowCustom
            />
          </div>
          <Button type="submit">{t('common.query')}</Button>
        </form>
      </section>

      {/* zone 汇总树（大区→小区→子服）：上移至看板之上，替代原底部扁平表格（FR-55）。
          FR-107：外层 Card 降级为区段标题 + 轻分隔，汇总树本身不动。 */}
      <section className="space-y-3">
        <SectionHeader icon={<ListTree className="size-4" />} title={t('zones.summaryTitle')} />
        <AsyncSection
          isLoading={summary.isLoading || instances.isLoading}
          isError={summary.isError || instances.isError}
          error={summary.error ?? instances.error}
        >
          <ZoneSummaryTree tree={summaryTreeModel} />
        </AsyncSection>
      </section>

      {/* 归派看板（FR-35 + 安全化 FR-71）：FR-107 外层 Card 降级为区段标题 + 轻分隔；
          解锁改派开关挪到区段标题右槽，看板未指派池 / 分组容器 / 磁贴密度保留。 */}
      <section className="space-y-3">
        <SectionHeader
          icon={<LayoutGrid className="size-4" />}
          title={t('zones.kanbanTitle')}
          actions={
            <>
              <p className="text-sm text-muted-foreground">{t('zones.kanbanHint')}</p>
              {/* 解锁改派开关（FR-71）：默认只读，解锁后逐卡才出改派 / 取消入口 */}
              <Label
                htmlFor="unlock-reassign"
                className="flex items-center gap-2 text-sm text-muted-foreground"
              >
                <Checkbox
                  id="unlock-reassign"
                  aria-label={t('zones.unlockLabel')}
                  checked={unlocked}
                  onCheckedChange={(v) => setUnlocked(v === true)}
                />
                {t('zones.unlockLabel')}
              </Label>
            </>
          }
        />
        <AsyncSection
          isLoading={instances.isLoading || summary.isLoading}
          isError={instances.isError || summary.isError}
          error={instances.error ?? summary.error}
        >
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-[18rem_1fr]">
              {/* 左侧未指派池 */}
              <DropBucket
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
                          <ZoneBucketView
                            key={`${bucket.group}/${bucket.zone}`}
                            bucket={bucket}
                            unlocked={unlocked}
                            onReassign={setReassignTarget}
                            onUnassign={(ns, sid) => unassignMut.mutate({ namespace: ns, serverId: sid })}
                          />
                        ))}
                      </div>
                    </div>
                  ))
                )}
              </div>
            </div>
          </AsyncSection>
      </section>

      {/* 改派对话框（FR-71）：手输 serverId 复述确认；提交调既有 assignZone */}
      <ReassignDialog
        open={reassignTarget !== null}
        onOpenChange={(o) => {
          if (!o) setReassignTarget(null)
        }}
        instance={reassignTarget}
        currentNote={
          reassignTarget
            ? noteForServer(assignments.data ?? [], reassignTarget.namespace, reassignTarget.serverId)
            : ''
        }
        groupOptions={groupOptions}
        zoneOptions={zoneOptions}
        pending={assignMut.isPending}
        onConfirm={(params) => assignMut.mutate(params)}
      />
    </div>
  )
}

// 单个 zone 桶视图：标题为小区名 + 实例数，内含其卡片；解锁后逐卡注入改派 / 取消指派入口（FR-71）。
function ZoneBucketView({
  bucket,
  unlocked,
  onReassign,
  onUnassign,
}: {
  bucket: ZoneBucket
  unlocked: boolean
  onReassign: (instance: InstanceView) => void
  onUnassign: (namespace: string, serverId: string) => void
}) {
  const { t } = useTranslation()
  return (
    <DropBucket
      title={bucket.zone}
      meta={t('zones.unitServers', { count: bucket.instances.length })}
    >
      {bucket.instances.length === 0 ? (
        <p className="px-0.5 py-2 text-xs text-muted-foreground">{t('zones.dropHere')}</p>
      ) : (
        bucket.instances.map((i) => (
          <ServerCard
            key={`${i.namespace}/${i.serverId}`}
            instance={i}
            actions={
              unlocked ? (
                <div className="flex items-center gap-1">
                  <Button variant="outline" size="sm" onClick={() => onReassign(i)}>
                    {t('zones.reassignBtn')}
                  </Button>
                  {/* 取消指派：显式二次确认后才调 unassignZone（FR-71） */}
                  <AlertDialog>
                    <AlertDialogTrigger asChild>
                      <Button variant="ghost" size="sm">
                        {t('zones.unassignBtn')}
                      </Button>
                    </AlertDialogTrigger>
                    <AlertDialogContent>
                      <AlertDialogHeader>
                        <AlertDialogTitle>
                          {t('zones.unassignConfirmTitle', { serverId: i.serverId })}
                        </AlertDialogTitle>
                        <AlertDialogDescription>
                          {t('zones.unassignConfirmDesc')}
                        </AlertDialogDescription>
                      </AlertDialogHeader>
                      <AlertDialogFooter>
                        <AlertDialogCancel>{t('common.cancel')}</AlertDialogCancel>
                        <AlertDialogAction onClick={() => onUnassign(i.namespace, i.serverId)}>
                          {t('zones.unassignConfirmAction')}
                        </AlertDialogAction>
                      </AlertDialogFooter>
                    </AlertDialogContent>
                  </AlertDialog>
                </div>
              ) : undefined
            }
          />
        ))
      )}
    </DropBucket>
  )
}
