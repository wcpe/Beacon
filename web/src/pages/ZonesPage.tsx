// 区分配页（看板式归派，FR-35 + 安全化 FR-71）：
// 左侧未指派 server 卡片池 + 右侧按大区(group)分组的 zone 容器。
// FR-71 取消「拖拽即改」：看板默认只读，须显式「解锁改派」后逐卡走「改派」对话框（手输 serverId 复述）/「取消指派」二次确认；
// 安全由后端排空门兜底（在线非空服改区返 409 ZONE_SERVER_ONLINE_NONEMPTY），前端摩擦只防误触（ADR-0036）。
// 保留「新增 区 / 指派」表单入口（用于建空区的首次指派）+ 区维度汇总。
// 指派表单的环境 / serverId / 大区 / 小区为从 API 拉取的下拉（serverId 仅列 bukkit 子服）并加非法值校验（增强 FR-40/FR-51）；备注仍为自由文本。

import { useDeferredValue, useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  AlertTriangle,
  Boxes,
  CheckCircle2,
  Clock3,
  LayoutGrid,
  ListTree,
  MoveRight,
  Server,
  ShieldCheck,
} from 'lucide-react'
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
import type { AssignmentView, InstanceView, ZoneStatView } from '../api/types'
import { useMessage } from '../components/useMessage'
import { usePageHeader } from '@/components/PageHeader'
import { useEnvironment } from '@/state/environment'
import { AsyncSection } from '@beacon/ui'
import { Badge } from '@beacon/ui'
import { Button } from '@beacon/ui'
import { Checkbox } from '@beacon/ui'
import { Input } from '@beacon/ui'
import { Label } from '@beacon/ui'
import { SectionHeader } from '@beacon/ui'
import { Combobox } from '@beacon/ui'
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
} from '@beacon/ui'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@beacon/ui'
import { buildKanbanModel, noteForServer, type ZoneBucket } from './zones/kanbanModel'
import { buildSummaryTree } from './zones/summaryTree'
import ServerCard from './zones/ServerCard'
import DropBucket from './zones/DropBucket'
import ReassignDialog from './zones/ReassignDialog'
import ZoneSummaryTree from './zones/ZoneSummaryTree'
import { filterInstancesByKeyword, getVisibleWindow } from '@/lib/instanceFiltering'
import { cn } from '@/lib/utils'

// 指派/汇总共用的过滤条件
interface ZoneFilter {
  namespace?: string
  group?: string
  zone?: string
}

// 新增 zone / 指派表单初值
const EMPTY_FORM = { namespace: '', serverId: '', group: '', zone: '', note: '' }
const ZONE_BUCKET_LIMIT = 30
const ZONE_SEARCH_LIMIT = 120
const SUMMARY_TREE_LIMIT = 8
const ZONE_CAPACITY_BASE = 128

// 排空门错误码（与后端 apperr.ErrZoneServerOnlineNonempty 一致，FR-71/ADR-0036）
const ERR_ZONE_SERVER_ONLINE_NONEMPTY = 'ZONE_SERVER_ONLINE_NONEMPTY'

interface ZoneMatrixRow {
  key: string
  group: string
  zone: string
  serverCount: number
  onlineCount: number
  utilization: number
  bucket: ZoneBucket
}

function zoneKey(group: string, zone: string): string {
  return `${group}\n${zone}`
}

function buildZoneRows(
  model: { groups: Array<{ zones: ZoneBucket[] }> },
  summary: ZoneStatView[],
): ZoneMatrixRow[] {
  const statMap = new Map(summary.map((s) => [zoneKey(s.group, s.zone), s]))
  return model.groups.flatMap((group) =>
    group.zones.map((bucket) => {
      const stat = statMap.get(zoneKey(bucket.group, bucket.zone))
      const serverCount = stat?.serverCount ?? bucket.instances.length
      const onlineCount =
        stat?.onlineCount ?? bucket.instances.filter((i) => i.status === 'online').length
      return {
        key: zoneKey(bucket.group, bucket.zone),
        group: bucket.group,
        zone: bucket.zone,
        serverCount,
        onlineCount,
        utilization: serverCount > 0 ? onlineCount / serverCount : 0,
        bucket,
      }
    }),
  )
}

function ZoneMetricTile({
  label,
  value,
  icon,
  tone = 'default',
}: {
  label: string
  value: string | number
  icon: ReactNode
  tone?: 'default' | 'danger' | 'success'
}) {
  return (
    <div className="rounded-md border bg-background p-3 shadow-sm">
      <div className="flex items-center justify-between text-xs text-muted-foreground">
        <span>{label}</span>
        <span
          className={cn(
            'text-primary',
            tone === 'danger' && 'text-orange-600',
            tone === 'success' && 'text-green-600',
          )}
        >
          {icon}
        </span>
      </div>
      <div className="mt-2 text-2xl font-semibold tracking-normal">{value}</div>
    </div>
  )
}

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
  const [serverKeyword, setServerKeyword] = useState('')
  const deferredServerKeyword = useDeferredValue(serverKeyword)
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
  const [selectedZoneKey, setSelectedZoneKey] = useState('')
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
    queryFn: () =>
      listAssignments(effectiveFilter.namespace, effectiveFilter.group, effectiveFilter.zone),
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
  const nsOptions = useMemo(() => namespaceOptions(namespacesQuery.data), [namespacesQuery.data])
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
  const kanbanInstances = useMemo(
    () => filterInstancesByKeyword(instances.data ?? [], deferredServerKeyword),
    [deferredServerKeyword, instances.data],
  )
  const model = useMemo(
    () => buildKanbanModel(kanbanInstances, summary.data ?? []),
    [kanbanInstances, summary.data],
  )
  const zoneBucketLimit = deferredServerKeyword.trim() ? ZONE_SEARCH_LIMIT : ZONE_BUCKET_LIMIT
  const unassignedWindow = useMemo(
    () => getVisibleWindow(model.unassigned, zoneBucketLimit),
    [model.unassigned, zoneBucketLimit],
  )

  // 由 summary + 看板模型派生汇总树（大区→小区→子服；计数取自 summary，与原表口径一致，FR-55）
  const summaryTreeModel = useMemo(
    () => buildSummaryTree(summary.data ?? [], model),
    [summary.data, model],
  )
  const zoneRows = useMemo(() => buildZoneRows(model, summary.data ?? []), [model, summary.data])
  const activeZoneKey = zoneRows.some((row) => row.key === selectedZoneKey)
    ? selectedZoneKey
    : zoneRows[0]?.key || ''
  const selectedZoneRow = zoneRows.find((row) => row.key === activeZoneKey) ?? zoneRows[0] ?? null
  const totalZoneCount = zoneRows.length
  const onlineServers = zoneRows.reduce((sum, row) => sum + row.onlineCount, 0)
  const totalServers = zoneRows.reduce((sum, row) => sum + row.serverCount, 0)
  const riskZones = zoneRows.filter((row) => row.utilization >= 0.8 || row.serverCount === 0).length
  const recentAssignments = useMemo(
    () =>
      [...(assignments.data ?? [])]
        .sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
        .slice(0, 5),
    [assignments.data],
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
    // 非法值拦截：环境 / serverId / 大区须落在 API 拉来的候选内（防手改 DOM 或脏缓存提交越界值）。
    // 小区（zone）例外：它是要新建的区名，允许候选外新值，否则全新集群无任何区时无法创建首个区。
    if (
      !namespaceCodes.includes(form.namespace) ||
      !serverOptions.includes(form.serverId) ||
      !groupOptions.includes(form.group)
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
          <p className="text-sm text-muted-foreground">{t('zones.assignDialogDesc')}</p>
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
              {/* 可编辑：小区是要新建的区名，须允许键入候选外的新值——否则全新集群无任何区、
                  候选恒空，「指派首个 server 即创建该区」无从落地（修复首个区无法创建）。 */}
              <Combobox
                id="a-zone"
                aria-label={t('common.zone')}
                value={form.zone}
                onChange={(v) => setForm({ ...form, zone: v })}
                options={zoneOptions}
                allowCustom
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
    <div className="grid h-full min-h-0 grid-rows-[auto_auto_minmax(0,1fr)] gap-3 overflow-hidden">
      <form
        onSubmit={onSearch}
        className="flex shrink-0 flex-wrap items-center gap-2 rounded-md border bg-background p-3 shadow-sm"
      >
        <Combobox
          id="f-group"
          aria-label={t('common.group')}
          className="w-40"
          value={fGroup}
          onChange={setFGroup}
          options={groupOptions}
          allowCustom
          placeholder={t('common.group')}
        />
        <Combobox
          id="f-zone"
          aria-label={t('common.zone')}
          className="w-40"
          value={fZone}
          onChange={setFZone}
          options={zoneOptions}
          allowCustom
          placeholder={t('common.zone')}
        />
        <Input
          id="f-server"
          aria-label={t('zones.searchServer')}
          className="w-52"
          value={serverKeyword}
          placeholder={t('zones.searchServerPlaceholder')}
          onChange={(e) => setServerKeyword(e.target.value)}
        />
        <span className="text-sm tabular-nums text-muted-foreground">
          {t('zones.visibleCount', {
            visible: kanbanInstances.length,
            total: instances.data?.length ?? 0,
          })}
        </span>
        <Button type="submit">{t('common.query')}</Button>
        <div className="ml-auto flex items-center gap-2">
          <Button type="button" variant="outline" onClick={invalidate}>
            {t('zones.refresh')}
          </Button>
        </div>
      </form>

      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-6">
        <ZoneMetricTile
          label={t('zones.metricZones')}
          value={totalZoneCount}
          icon={<Boxes className="size-4" />}
        />
        <ZoneMetricTile
          label={t('zones.metricOnline')}
          value={onlineServers}
          icon={<CheckCircle2 className="size-4" />}
          tone="success"
        />
        <ZoneMetricTile
          label={t('zones.metricTotalServers')}
          value={totalServers}
          icon={<Server className="size-4" />}
        />
        <ZoneMetricTile
          label={t('zones.metricUnassigned')}
          value={model.unassigned.length}
          icon={<AlertTriangle className="size-4" />}
          tone={model.unassigned.length > 0 ? 'danger' : 'success'}
        />
        <ZoneMetricTile
          label={t('zones.metricRisk')}
          value={riskZones}
          icon={<ShieldCheck className="size-4" />}
          tone={riskZones > 0 ? 'danger' : 'success'}
        />
        <ZoneMetricTile
          label={t('zones.metricRecent')}
          value={recentAssignments[0]?.updatedAt ? t('zones.recentUpdated') : '-'}
          icon={<Clock3 className="size-4" />}
        />
      </div>

      <div className="grid min-h-0 grid-cols-[20rem_minmax(0,1fr)_24rem] grid-rows-[minmax(0,1fr)_8.5rem] gap-3 overflow-hidden">
        <AsyncSection
          isLoading={instances.isLoading || summary.isLoading}
          isError={instances.isError || summary.isError}
          error={instances.error ?? summary.error}
        >
          <aside
            role="region"
            aria-label={t('zones.summaryScrollRegion')}
            className="h-full min-h-0 space-y-3 overflow-y-auto rounded-md border bg-background p-3 shadow-sm"
          >
            <div className="flex items-center gap-2">
              <ListTree className="size-4 text-primary" />
              <h2 className="text-sm font-semibold">{t('zones.summaryTitle')}</h2>
            </div>
            <ZoneSummaryTree tree={summaryTreeModel} serverLimitPerZone={SUMMARY_TREE_LIMIT} />
            <DropBucket
              title={t('zones.unassignedTitle')}
              meta={t('zones.unitServers', { count: model.unassigned.length })}
            >
              {model.unassigned.length === 0 ? (
                <p className="px-0.5 py-2 text-xs text-muted-foreground">
                  {t('zones.noUnassigned')}
                </p>
              ) : (
                <>
                  {unassignedWindow.items.map((i) => (
                    <ServerCard key={`${i.namespace}/${i.serverId}`} instance={i} />
                  ))}
                  {unassignedWindow.hidden > 0 && (
                    <p className="px-0.5 py-1 text-xs text-muted-foreground">
                      {t('zones.moreServers', { count: unassignedWindow.hidden })}
                    </p>
                  )}
                </>
              )}
            </DropBucket>
          </aside>

          <div
            role="region"
            aria-label={t('zones.matrixScrollRegion')}
            className="flex h-full min-h-0 flex-col overflow-hidden rounded-md border bg-background shadow-sm"
          >
            <div className="flex h-9 shrink-0 items-center gap-3 border-b px-3">
              <LayoutGrid className="size-4 text-primary" />
              <h2 className="text-sm font-semibold">{t('zones.allocationTitle')}</h2>
              <div className="ml-auto flex items-center gap-3">
                <span className="text-xs text-muted-foreground">{t('zones.kanbanHint')}</span>
                <Label
                  htmlFor="unlock-reassign"
                  className="flex items-center gap-2 text-xs text-muted-foreground"
                >
                  <Checkbox
                    id="unlock-reassign"
                    aria-label={t('zones.unlockLabel')}
                    checked={unlocked}
                    onCheckedChange={(v) => setUnlocked(v === true)}
                  />
                  {t('zones.unlockLabel')}
                </Label>
              </div>
            </div>
            <div className="min-h-0 flex-1 overflow-auto">
              <table className="w-full min-w-[640px] text-sm">
                <thead className="sticky top-0 z-10 border-b bg-muted/40 text-xs text-muted-foreground">
                  <tr>
                    <th className="px-3 py-2 text-left">{t('common.group')}</th>
                    <th className="px-3 py-2 text-left">{t('common.zone')}</th>
                    <th className="px-3 py-2 text-left">{t('zones.colCapacity')}</th>
                    <th className="px-3 py-2 text-left">{t('zones.colOnline')}</th>
                    <th className="px-3 py-2 text-left">{t('zones.colRisk')}</th>
                  </tr>
                </thead>
                <tbody>
                  {zoneRows.map((row) => (
                    <tr
                      key={row.key}
                      role="button"
                      tabIndex={0}
                      aria-current={row.key === activeZoneKey ? 'true' : undefined}
                      aria-label={t('zones.selectZoneRow', { group: row.group, zone: row.zone })}
                      onClick={() => setSelectedZoneKey(row.key)}
                      onKeyDown={(e) => {
                        if (e.key !== 'Enter' && e.key !== ' ') return
                        e.preventDefault()
                        setSelectedZoneKey(row.key)
                      }}
                      className={cn(
                        'cursor-pointer border-b outline-none last:border-0 hover:bg-muted/40 focus-visible:bg-muted/60',
                        row.key === activeZoneKey && 'bg-primary/5',
                      )}
                    >
                      <td className="px-3 py-2">{row.group}</td>
                      <td className="px-3 py-2 font-medium">{row.zone}</td>
                      <td className="px-3 py-2">
                        <div className="flex items-center gap-2">
                          <span className="w-12 font-mono">{ZONE_CAPACITY_BASE}/s</span>
                          <span className="h-1.5 w-24 overflow-hidden rounded-full bg-muted">
                            <span
                              className="block h-full rounded-full bg-primary"
                              style={{ width: `${Math.min(100, row.utilization * 100)}%` }}
                            />
                          </span>
                        </div>
                      </td>
                      <td className="px-3 py-2">
                        {row.onlineCount} / {row.serverCount}
                      </td>
                      <td className="px-3 py-2">
                        <Badge
                          variant={
                            row.utilization >= 0.8 || row.serverCount === 0
                              ? 'destructive'
                              : 'secondary'
                          }
                        >
                          {row.utilization >= 0.8 || row.serverCount === 0
                            ? t('zones.riskWarn')
                            : t('zones.riskGood')}
                        </Badge>
                      </td>
                    </tr>
                  ))}
                  {zoneRows.length === 0 && (
                    <tr>
                      <td className="px-3 py-8 text-center text-muted-foreground" colSpan={5}>
                        {t('zones.noZones')}
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </div>

          <aside
            role="region"
            aria-label={t('zones.detailScrollRegion')}
            className="row-span-2 h-full min-h-0 overflow-y-auto rounded-md border bg-background p-3 shadow-sm"
          >
            <ZoneDetailPanel
              row={selectedZoneRow}
              unlocked={unlocked}
              limit={zoneBucketLimit}
              onReassign={setReassignTarget}
              onUnassign={(ns, sid) => unassignMut.mutate({ namespace: ns, serverId: sid })}
            />
          </aside>
        </AsyncSection>

        <section className="col-span-2 flex min-h-0 flex-col gap-2 overflow-hidden">
          <SectionHeader
            icon={<MoveRight className="size-4" />}
            title={t('zones.recentAssignmentsTitle')}
          />
          <RecentAssignmentTable rows={recentAssignments} />
        </section>
      </div>

      {/* 改派对话框（FR-71）：手输 serverId 复述确认；提交调既有 assignZone */}
      <ReassignDialog
        open={reassignTarget !== null}
        onOpenChange={(o) => {
          if (!o) setReassignTarget(null)
        }}
        instance={reassignTarget}
        currentNote={
          reassignTarget
            ? noteForServer(
                assignments.data ?? [],
                reassignTarget.namespace,
                reassignTarget.serverId,
              )
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

function ZoneDetailPanel({
  row,
  unlocked,
  onReassign,
  onUnassign,
  limit,
}: {
  row: ZoneMatrixRow | null
  unlocked: boolean
  limit: number
  onReassign: (instance: InstanceView) => void
  onUnassign: (namespace: string, serverId: string) => void
}) {
  const { t } = useTranslation()
  if (!row) {
    return <p className="text-sm text-muted-foreground">{t('zones.noZones')}</p>
  }
  return (
    <div className="space-y-3">
      <div>
        <div className="flex items-center gap-2">
          <h2 className="text-sm font-semibold">
            {row.group} / {row.zone}
          </h2>
          <Badge
            variant={row.utilization >= 0.8 || row.serverCount === 0 ? 'destructive' : 'secondary'}
          >
            {row.utilization >= 0.8 || row.serverCount === 0
              ? t('zones.riskWarn')
              : t('zones.riskGood')}
          </Badge>
        </div>
        <p className="mt-1 text-xs text-muted-foreground">{t('zones.detailSubtitle')}</p>
      </div>
      <dl className="grid grid-cols-[6rem_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-xs">
        <dt className="text-muted-foreground">{t('zones.colCapacity')}</dt>
        <dd>{ZONE_CAPACITY_BASE}/s</dd>
        <dt className="text-muted-foreground">{t('zones.colOnline')}</dt>
        <dd>
          {row.onlineCount} / {row.serverCount}
        </dd>
        <dt className="text-muted-foreground">{t('zones.detailUtilization')}</dt>
        <dd>{Math.round(row.utilization * 100)}%</dd>
        <dt className="text-muted-foreground">{t('zones.detailServers')}</dt>
        <dd>{row.bucket.instances.length}</dd>
      </dl>
      <ZoneBucketView
        bucket={row.bucket}
        unlocked={unlocked}
        limit={limit}
        onReassign={onReassign}
        onUnassign={onUnassign}
      />
    </div>
  )
}

function RecentAssignmentTable({ rows }: { rows: AssignmentView[] }) {
  const { t } = useTranslation()
  return (
    <div className="min-h-0 flex-1 overflow-auto rounded-md border bg-background shadow-sm">
      <table className="w-full min-w-[720px] text-sm">
        <thead className="border-b bg-muted/40 text-xs text-muted-foreground">
          <tr>
            <th className="px-3 py-2 text-left">{t('common.serverId')}</th>
            <th className="px-3 py-2 text-left">{t('common.namespace')}</th>
            <th className="px-3 py-2 text-left">{t('common.group')}</th>
            <th className="px-3 py-2 text-left">{t('common.zone')}</th>
            <th className="px-3 py-2 text-left">{t('common.note')}</th>
            <th className="px-3 py-2 text-left">{t('zones.colUpdatedAt')}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={`${row.namespace}/${row.serverId}`} className="border-b last:border-0">
              <td className="px-3 py-2 font-mono">{row.serverId}</td>
              <td className="px-3 py-2">{row.namespace}</td>
              <td className="px-3 py-2">{row.group}</td>
              <td className="px-3 py-2">{row.zone}</td>
              <td className="px-3 py-2">{row.note || '-'}</td>
              <td className="px-3 py-2">{row.updatedAt || '-'}</td>
            </tr>
          ))}
          {rows.length === 0 && (
            <tr>
              <td className="px-3 py-6 text-center text-muted-foreground" colSpan={6}>
                {t('zones.noRecentAssignments')}
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  )
}

// 单个 zone 桶视图：标题为小区名 + 实例数，内含其卡片；解锁后逐卡注入改派 / 取消指派入口（FR-71）。
function ZoneBucketView({
  bucket,
  unlocked,
  onReassign,
  onUnassign,
  limit,
}: {
  bucket: ZoneBucket
  unlocked: boolean
  limit: number
  onReassign: (instance: InstanceView) => void
  onUnassign: (namespace: string, serverId: string) => void
}) {
  const { t } = useTranslation()
  const visible = getVisibleWindow(bucket.instances, limit)
  return (
    <DropBucket
      title={bucket.zone}
      meta={t('zones.unitServers', { count: bucket.instances.length })}
    >
      {bucket.instances.length === 0 ? (
        <p className="px-0.5 py-2 text-xs text-muted-foreground">{t('zones.dropHere')}</p>
      ) : (
        <>
          {visible.items.map((i) => (
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
          ))}
          {visible.hidden > 0 && (
            <p className="px-0.5 py-1 text-xs text-muted-foreground">还有 {visible.hidden} 台</p>
          )}
        </>
      )}
    </DropBucket>
  )
}
