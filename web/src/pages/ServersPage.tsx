// 服务器页（FR-65）：合并原「实例与健康」（FR-49）+「代理服管理」（FR-52）为统一服务器视图。
// 统一列出全部实例（bukkit+bungee，不限 role）：role / group / zone（未分配黄高亮）/ status / address / version
// + 角色相关列（bukkit 人数·TPS；bungee 连接·运行时长·后端可达）+ 最近心跳 + 操作。
// 操作：下线/取消下线（FR-49）、drain/undrain（FR-10）、区改派（复用 FR-71 ReassignDialog，含排空门 + 手输确认）。
// 点行从右侧滑出单服详情 Sheet（按 role 分区展示深指标 + 关系）。5 秒轮询健康。

import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  ApiClientError,
  assignZone,
  drainInstance,
  listAssignments,
  listDefaultEntries,
  listDrains,
  listInstances,
  listNamespaces,
  listOfflineInstances,
  offlineInstance,
  onlineInstance,
  undrainInstance,
  zoneSummary,
} from '../api/client'
import type { AssignParams, InstanceFilter } from '../api/client'
import type { InstanceView } from '../api/types'
import { formatTime, namespaceOptions } from '../api/format'
import StatusBadge from '../components/StatusBadge'
import RoleBadge from '../components/RoleBadge'
import { useMessage } from '../components/useMessage'
import AsyncSection from '@/components/AsyncSection'
import DataTable, { type DataTableColumn } from '@/components/DataTable'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'
import { Combobox } from '@/components/ui/combobox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
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
import ReassignDialog from './zones/ReassignDialog'
import ServerDetailSheet from './servers/ServerDetailSheet'
import AddServerWizard from './servers/AddServerWizard'

// 健康轮询周期（毫秒）
const REFETCH_MS = 5000

// Radix Select 不允许空串值，"全部"用哨兵值 all 表示，提交时转 undefined
const ALL = 'all'

// bungee 角色编码（与后端 role 约定一致）
const ROLE_BUNGEE = 'bungee'

// 排空门错误码（与后端 apperr.ErrZoneServerOnlineNonempty 一致，FR-71/ADR-0036）
const ERR_ZONE_SERVER_ONLINE_NONEMPTY = 'ZONE_SERVER_ONLINE_NONEMPTY'

// 角色相关「人数/连接」列：bukkit 显在线人数，bungee 显代理在线连接。
function loadCell(i: InstanceView): string {
  if (i.role === ROLE_BUNGEE) return String(i.proxy.onlineConnections)
  return String(i.playerCount)
}

// 角色相关「TPS/后端可达」列：bukkit 显 TPS，bungee 显后端可达 up/total（无后端显「无后端」），其余 '-'。
function rateCell(t: TFunction, i: InstanceView): string {
  if (i.role === ROLE_BUNGEE) {
    return i.proxy.backendTotal > 0
      ? `${i.proxy.backendUp} / ${i.proxy.backendTotal}`
      : t('servers.noBackend')
  }
  return i.tps.toFixed(1)
}

export default function ServersPage() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const msg = useMessage()

  const [namespace, setNamespace] = useState('')
  const [group, setGroup] = useState('')
  const [zone, setZone] = useState('')
  const [role, setRole] = useState(ALL)
  const [status, setStatus] = useState(ALL)
  const [filter, setFilter] = useState<InstanceFilter>({})

  // 详情 Sheet 选中的实例（null 表示关闭）
  const [detailInstance, setDetailInstance] = useState<InstanceView | null>(null)
  // 当前正在改派的实例（null 表示改派对话框关闭）
  const [reassignTarget, setReassignTarget] = useState<InstanceView | null>(null)
  // 新服接入引导向导开关（FR-85）
  const [wizardOpen, setWizardOpen] = useState(false)

  const { data, isLoading, isError, error, isFetching } = useQuery({
    queryKey: ['instances', filter],
    queryFn: () => listInstances(filter),
    refetchInterval: REFETCH_MS,
  })

  // 主动下线标记（FR-49）：已下线实例不在注册表列表出现，单列展示并提供「取消下线」。
  const { data: offlineMarkers } = useQuery({
    queryKey: ['offline-instances', filter.namespace],
    queryFn: () => listOfflineInstances(filter.namespace),
    refetchInterval: REFETCH_MS,
  })

  // 排空标记（FR-10）：用于在表内标 drain 态并切换 drain/undrain 操作。
  const { data: drains } = useQuery({
    queryKey: ['drains', filter.namespace],
    queryFn: () => listDrains(filter.namespace),
    refetchInterval: REFETCH_MS,
  })

  // 各小区默认入口（FR-48）：供 bungee 详情展示所属小区默认入口；按 (namespace, group, zone) 复合键索引。
  const { data: defaultEntries } = useQuery({
    queryKey: ['default-entries', filter.namespace],
    queryFn: () => listDefaultEntries(filter.namespace),
    refetchInterval: REFETCH_MS,
  })

  // 现有指派（改派对话框沿用备注，避免改派清空运维填写的备注）。
  const { data: assignments } = useQuery({
    queryKey: ['assignments', filter.namespace],
    queryFn: () => listAssignments(filter.namespace),
  })

  // 筛选维度下拉的候选来源（FR-51）：环境来自 listNamespaces，大区 / 小区由 zone 汇总与全量实例派生。
  // 候选不随当前过滤收窄（全量拉取），且筛选框允许键入候选外的值（可编辑）。
  const namespacesQuery = useQuery({ queryKey: ['namespaces'], queryFn: () => listNamespaces() })
  const allInstancesQuery = useQuery({
    queryKey: ['instances', 'filter-options'],
    queryFn: () => listInstances({}),
  })
  const zoneSummaryQuery = useQuery({ queryKey: ['zone-summary', 'all'], queryFn: () => zoneSummary() })

  // 候选显示「编码 · 名称」，真实值仍是 code（FR-70）
  const nsOptions = useMemo(() => namespaceOptions(namespacesQuery.data), [namespacesQuery.data])
  // 大区候选：zone 汇总与实例列表去重并集（兼容无 zone 指派但已注册的大区）
  const groupOptions = useMemo(() => {
    const set = new Set<string>()
    for (const z of zoneSummaryQuery.data ?? []) if (z.group) set.add(z.group)
    for (const i of allInstancesQuery.data ?? []) if (i.group) set.add(i.group)
    return Array.from(set).sort()
  }, [zoneSummaryQuery.data, allInstancesQuery.data])
  // 小区候选：zone 汇总与实例列表去重并集
  const zoneOptions = useMemo(() => {
    const set = new Set<string>()
    for (const z of zoneSummaryQuery.data ?? []) if (z.zone) set.add(z.zone)
    for (const i of allInstancesQuery.data ?? []) if (i.zone) set.add(i.zone)
    return Array.from(set).sort()
  }, [zoneSummaryQuery.data, allInstancesQuery.data])

  // 当前排空集合（namespace/serverId 复合键）：跨 namespace 列实例，须复合键避免同名 serverId 误判。
  const drainedSet = useMemo(() => {
    const set = new Set<string>()
    for (const d of drains ?? []) set.add(`${d.namespace}/${d.serverId}`)
    return set
  }, [drains])

  // (namespace, group, zone) → 默认入口 serverId 映射：同名 zone 在不同大区是不同小区，须复合键防串值。
  const entryByZone = useMemo(() => {
    const map = new Map<string, string>()
    for (const e of defaultEntries ?? []) {
      map.set(`${e.namespace}/${e.group}/${e.zone}`, e.defaultServerId)
    }
    return map
  }, [defaultEntries])

  // 区分排空门 409 与一般错误：在线非空服改区被硬拒时给「先排空」专属中文提示（FR-71/ADR-0036）
  function reportError(e: unknown) {
    if (e instanceof ApiClientError && e.code === ERR_ZONE_SERVER_ONLINE_NONEMPTY) {
      msg.showError(t('zones.drainGateHint'))
      return
    }
    msg.showError(e instanceof Error ? e.message : t('common.unknownError'))
  }

  // 主动下线：namespace 取自该行实例，不再强制先在过滤条件中选环境（FR-49）。
  const offlineMut = useMutation({
    mutationFn: ({ serverId, namespace: ns }: { serverId: string; namespace: string }) =>
      offlineInstance(serverId, ns),
    onSuccess: (_d, { serverId }) => {
      msg.showSuccess(t('servers.msgOffline', { serverId }))
      qc.invalidateQueries({ queryKey: ['instances'] })
      qc.invalidateQueries({ queryKey: ['offline-instances'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  // 取消主动下线：清除拒绝态，使实例可重新接入（FR-49）。
  const onlineMut = useMutation({
    mutationFn: ({ serverId, namespace: ns }: { serverId: string; namespace: string }) =>
      onlineInstance(serverId, ns),
    onSuccess: (_d, { serverId }) => {
      msg.showSuccess(t('servers.msgCancelOffline', { serverId }))
      qc.invalidateQueries({ queryKey: ['instances'] })
      qc.invalidateQueries({ queryKey: ['offline-instances'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  // 标记排空（FR-10）：仅落位决策降权，不踢玩家。
  const drainMut = useMutation({
    mutationFn: ({ serverId, namespace: ns }: { serverId: string; namespace: string }) =>
      drainInstance(serverId, ns),
    onSuccess: (_d, { serverId }) => {
      msg.showSuccess(t('servers.msgDrained', { serverId }))
      qc.invalidateQueries({ queryKey: ['drains'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  // 取消排空（FR-10）。
  const undrainMut = useMutation({
    mutationFn: ({ serverId, namespace: ns }: { serverId: string; namespace: string }) =>
      undrainInstance(serverId, ns),
    onSuccess: (_d, { serverId }) => {
      msg.showSuccess(t('servers.msgUndrained', { serverId }))
      qc.invalidateQueries({ queryKey: ['drains'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  // 区改派（FR-71）：复用 ReassignDialog 提交的完整入参调既有 assignZone。
  const assignMut = useMutation({
    mutationFn: (params: AssignParams) => assignZone(params),
    onSuccess: (a) => {
      msg.showSuccess(t('servers.msgReassigned', { serverId: a.serverId, zone: a.zone }))
      setReassignTarget(null)
      qc.invalidateQueries({ queryKey: ['instances'] })
      qc.invalidateQueries({ queryKey: ['assignments'] })
      qc.invalidateQueries({ queryKey: ['zone-summary'] })
    },
    onError: reportError,
  })

  function onSearch(e: React.FormEvent) {
    e.preventDefault()
    setFilter({
      namespace: namespace.trim() || undefined,
      group: group.trim() || undefined,
      zone: zone.trim() || undefined,
      role: role === ALL ? undefined : role,
      status: status === ALL ? undefined : status,
    })
  }

  // 取该实例现有指派备注（改派对话框沿用，避免清空运维填写的备注）。
  function noteFor(i: InstanceView): string {
    const a = (assignments ?? []).find((x) => x.namespace === i.namespace && x.serverId === i.serverId)
    return a?.note ?? ''
  }

  // 实例表列定义（操作列闭包引用各 mutation / state，故在组件内定义）。
  const columns: DataTableColumn<InstanceView>[] = [
    { header: 'serverId', className: 'font-mono', cell: (i) => i.serverId },
    { header: t('servers.colNamespace'), cell: (i) => i.namespace },
    { header: t('servers.colRole'), cell: (i) => <RoleBadge role={i.role} /> },
    { header: t('servers.colGroup'), cell: (i) => i.group },
    {
      header: t('servers.colZone'),
      cell: (i) =>
        i.zone === null ? (
          <Badge variant="outline" className="border-amber-500 text-amber-600">
            {t('servers.unassignedBadge')}
          </Badge>
        ) : (
          i.zone
        ),
    },
    { header: t('servers.colStatus'), cell: (i) => <StatusBadge status={i.status} reason={i.healthReason} /> },
    { header: t('servers.colAddress'), className: 'font-mono', cell: (i) => i.address },
    { header: t('servers.colVersion'), cell: (i) => i.version },
    // 角色相关：bukkit 人数 / bungee 连接
    { header: t('servers.colLoad'), cell: (i) => loadCell(i) },
    // 角色相关：bukkit TPS / bungee 后端可达
    { header: t('servers.colRate'), cell: (i) => rateCell(t, i) },
    {
      header: t('servers.colDrain'),
      cell: (i) =>
        drainedSet.has(`${i.namespace}/${i.serverId}`) ? (
          <Badge variant="outline" className="border-amber-500 text-amber-600">
            {t('servers.drainedBadge')}
          </Badge>
        ) : (
          '-'
        ),
    },
    { header: t('servers.colLastHeartbeat'), cell: (i) => formatTime(i.lastHeartbeat) },
    {
      header: t('servers.colActions'),
      cell: (i) => {
        const drained = drainedSet.has(`${i.namespace}/${i.serverId}`)
        // 操作列各按钮 stopPropagation：避免触发行点击（打开详情 Sheet）。
        const stop = (e: React.MouseEvent) => e.stopPropagation()
        return (
          <div className="flex items-center gap-1" onClick={stop}>
            {/* 下线（FR-49）：二次确认后调 offlineInstance */}
            <AlertDialog>
              <AlertDialogTrigger asChild>
                <Button variant="destructive" size="sm" disabled={offlineMut.isPending}>
                  {t('servers.offlineBtn')}
                </Button>
              </AlertDialogTrigger>
              <AlertDialogContent onClick={stop}>
                <AlertDialogHeader>
                  <AlertDialogTitle>{t('servers.offlineConfirmTitle', { serverId: i.serverId })}</AlertDialogTitle>
                  <AlertDialogDescription>
                    {t('servers.offlineConfirmDesc', { namespace: i.namespace })}
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>{t('common.cancel')}</AlertDialogCancel>
                  <AlertDialogAction
                    onClick={() => offlineMut.mutate({ serverId: i.serverId, namespace: i.namespace })}
                  >
                    {t('servers.offlineConfirmAction')}
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
            {/* drain / undrain（FR-10）：按当前排空态切换 */}
            {drained ? (
              <Button
                variant="outline"
                size="sm"
                disabled={undrainMut.isPending}
                onClick={() => undrainMut.mutate({ serverId: i.serverId, namespace: i.namespace })}
              >
                {t('servers.undrainBtn')}
              </Button>
            ) : (
              <Button
                variant="outline"
                size="sm"
                disabled={drainMut.isPending}
                onClick={() => drainMut.mutate({ serverId: i.serverId, namespace: i.namespace })}
              >
                {t('servers.drainBtn')}
              </Button>
            )}
            {/* 区改派（FR-71）：仅 bukkit 子服可指派进 zone（BC 代理不可，与后端校验一致 FR-8/FR-35） */}
            {i.role !== ROLE_BUNGEE && (
              <Button variant="ghost" size="sm" onClick={() => setReassignTarget(i)}>
                {t('servers.reassignBtn')}
              </Button>
            )}
          </div>
        )
      },
    },
  ]

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <h1 className="text-xl font-semibold">{t('servers.title')}</h1>
        {isFetching && <span className="text-sm text-muted-foreground">{t('common.refreshing')}</span>}
        {/* 新服接入引导向导入口（FR-85） */}
        <Button className="ml-auto" onClick={() => setWizardOpen(true)}>
          {t('servers.wizardOpenBtn')}
        </Button>
      </div>

      <Card>
        <CardContent className="space-y-3">
          <form onSubmit={onSearch} className="flex flex-wrap items-end gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="f-namespace">{t('common.namespace')}</Label>
              {/* 筛选框：可编辑下拉，候选来自 API 但允许键入列表外值（FR-51） */}
              <Combobox
                id="f-namespace"
                aria-label={t('common.namespace')}
                className="w-40"
                value={namespace}
                onChange={setNamespace}
                options={nsOptions}
                allowCustom
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="f-group">{t('common.group')}</Label>
              <Combobox
                id="f-group"
                aria-label={t('common.group')}
                className="w-40"
                value={group}
                onChange={setGroup}
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
                value={zone}
                onChange={setZone}
                options={zoneOptions}
                allowCustom
              />
            </div>
            <div className="space-y-1.5">
              <Label>{t('common.role')}</Label>
              <Select value={role} onValueChange={setRole}>
                <SelectTrigger className="w-36">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ALL}>{t('servers.filterAll')}</SelectItem>
                  <SelectItem value="bukkit">bukkit</SelectItem>
                  <SelectItem value="bungee">bungee</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label>{t('common.status')}</Label>
              <Select value={status} onValueChange={setStatus}>
                <SelectTrigger className="w-36">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ALL}>{t('servers.filterAll')}</SelectItem>
                  <SelectItem value="online">online</SelectItem>
                  <SelectItem value="lost">lost</SelectItem>
                  <SelectItem value="offline">offline</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <Button type="submit">{t('common.query')}</Button>
          </form>
          <p className="text-sm text-muted-foreground">{t('servers.tip')}</p>
        </CardContent>
      </Card>

      <Card>
        <CardContent>
          <AsyncSection isLoading={isLoading} isError={isError} error={error}>
            <DataTable
              columns={columns}
              rows={data}
              rowKey={(i) => `${i.namespace}/${i.serverId}`}
              emptyText={t('servers.empty')}
              onRowClick={(i) => setDetailInstance(i)}
              rowClassName={(i) => (!i.assigned ? 'bg-amber-50' : undefined)}
            />
          </AsyncSection>
        </CardContent>
      </Card>

      {/* 已主动下线标记（FR-49）：已下线实例不在上表（已移出可用集），单列展示并支持取消下线 */}
      {offlineMarkers && offlineMarkers.length > 0 && (
        <Card>
          <CardContent className="space-y-3">
            <h2 className="text-base font-semibold">{t('servers.offlineSectionTitle')}</h2>
            <DataTable
              columns={[
                { header: 'serverId', className: 'font-mono', cell: (o) => o.serverId },
                { header: t('servers.colNamespace'), cell: (o) => o.namespace },
                { header: t('servers.offlineColReason'), cell: (o) => o.reason || '-' },
                {
                  header: t('servers.offlineColActions'),
                  cell: (o) => (
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={onlineMut.isPending}
                      onClick={() => onlineMut.mutate({ serverId: o.serverId, namespace: o.namespace })}
                    >
                      {t('servers.cancelOfflineBtn')}
                    </Button>
                  ),
                },
              ]}
              rows={offlineMarkers}
              rowKey={(o) => `${o.namespace}/${o.serverId}`}
              emptyText={t('servers.offlineEmpty')}
            />
          </CardContent>
        </Card>
      )}

      {/* 单服详情 Sheet：点行从右侧滑出，按 role 分区展示深指标 + 关系，不发新请求 */}
      <ServerDetailSheet
        instance={detailInstance}
        onOpenChange={(open) => !open && setDetailInstance(null)}
        defaultEntry={
          detailInstance && detailInstance.zone
            ? entryByZone.get(`${detailInstance.namespace}/${detailInstance.group}/${detailInstance.zone}`)
            : undefined
        }
      />

      {/* 改派对话框（FR-71）：手输 serverId 复述确认；提交调既有 assignZone */}
      <ReassignDialog
        open={reassignTarget !== null}
        onOpenChange={(o) => {
          if (!o) setReassignTarget(null)
        }}
        instance={reassignTarget}
        currentNote={reassignTarget ? noteFor(reassignTarget) : ''}
        groupOptions={groupOptions}
        zoneOptions={zoneOptions}
        pending={assignMut.isPending}
        onConfirm={(params) => assignMut.mutate(params)}
      />

      {/* 新服接入引导向导（FR-85）：填身份生成 agent 接入片段，可选预建 zone 指派 */}
      <AddServerWizard
        open={wizardOpen}
        onOpenChange={setWizardOpen}
        namespace={filter.namespace ?? ''}
        nsOptions={nsOptions}
        groupOptions={groupOptions}
      />
    </div>
  )
}
