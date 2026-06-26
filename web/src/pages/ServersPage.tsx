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
  triggerResync,
  undrainInstance,
  zoneSummary,
} from '../api/client'
import type { AssignParams, InstanceFilter } from '../api/client'
import type { InstanceView } from '../api/types'
import { formatTime, namespaceOptions } from '../api/format'
import { useEnvironment } from '@/state/environment'
import {
  buildMajorityVersions,
  isAgentVersionMismatch,
  type MajorityVersionByNamespace,
} from '@/lib/agentVersionConsistency'
import StatusBadge from '../components/StatusBadge'
import RoleBadge from '../components/RoleBadge'
import { useMessage } from '../components/useMessage'
import { usePageHeader } from '@/components/PageHeader'
import AsyncSection from '@/components/AsyncSection'
import { TableSkeleton } from '@/components/skeletons'
import DataTable, { type DataTableColumn } from '@/components/DataTable'
import SummaryStrip, { type SummaryItem } from '@/components/SummaryStrip'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
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
} from '@/components/ui/alert-dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
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

// 版本/agent 合一单元格（FR-106，合并原版本列 + agent 版本列 FR-86）：
// 显「子服版本 · agent 版本」，agent 版本空显「未知」弱色；与本环境多数 agent 版本不一致时整格黄框 + 悬浮提示。
function versionAgentCell(t: TFunction, i: InstanceView, majority: MajorityVersionByNamespace) {
  // agent 版本片段：空串回退「未知」
  const agentText = i.agentVersion || t('servers.agentVersionUnknown')
  const mismatch = i.agentVersion ? isAgentVersionMismatch(i, majority) : false
  if (mismatch) {
    // 沿用 FR-86 黄框：版本不一致整格高亮 + 原因悬浮
    return (
      <Badge
        variant="outline"
        className="border-amber-500 font-mono text-amber-600"
        title={t('servers.agentVersionMismatch')}
      >
        {i.version} · {agentText}
      </Badge>
    )
  }
  return (
    <span className="font-mono">
      {i.version} ·{' '}
      {i.agentVersion ? agentText : <span className="text-muted-foreground">{agentText}</span>}
    </span>
  )
}

export default function ServersPage() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const msg = useMessage()

  // 环境收口（FR-105 真机打磨）：环境改读页眉全局环境，不再页内自管 namespace 筛选；其它筛选维度（大区/小区/角色/状态）保留页内。
  const namespace = useEnvironment()
  const [group, setGroup] = useState('')
  const [zone, setZone] = useState('')
  const [role, setRole] = useState(ALL)
  const [status, setStatus] = useState(ALL)
  // 页内非环境筛选的已生效条件（不含 namespace；namespace 由全局环境合并）
  const [filter, setFilter] = useState<InstanceFilter>({})

  // 生效过滤 = 页内筛选 + 全局环境（空串＝全部环境，沿用「不传」语义）。
  // 全局环境变化即重算 effectiveFilter → 各查询 queryKey 含其 namespace → 自动重查。
  const effectiveFilter = useMemo<InstanceFilter>(
    () => ({ ...filter, namespace: namespace || undefined }),
    [filter, namespace],
  )

  // 详情 Sheet 选中的实例（null 表示关闭）
  const [detailInstance, setDetailInstance] = useState<InstanceView | null>(null)
  // 详情 Sheet 打开时是否自动触发取日志（「查看日志」入口置 true，「agent 详情」入口置 false）
  const [detailFocusLogs, setDetailFocusLogs] = useState(false)
  // 待确认下线的实例（null 表示确认弹窗关闭）：从行操作下拉菜单外层受控触发，避免菜单关闭吞掉弹窗
  const [offlineTarget, setOfflineTarget] = useState<InstanceView | null>(null)
  // 当前正在改派的实例（null 表示改派对话框关闭）
  const [reassignTarget, setReassignTarget] = useState<InstanceView | null>(null)
  // 新服接入引导向导开关（FR-85）
  const [wizardOpen, setWizardOpen] = useState(false)

  const { data, isLoading, isError, error, isFetching } = useQuery({
    queryKey: ['instances', effectiveFilter],
    queryFn: () => listInstances(effectiveFilter),
    refetchInterval: REFETCH_MS,
  })

  // 主动下线标记（FR-49）：已下线实例不在注册表列表出现，单列展示并提供「取消下线」。
  const { data: offlineMarkers } = useQuery({
    queryKey: ['offline-instances', effectiveFilter.namespace],
    queryFn: () => listOfflineInstances(effectiveFilter.namespace),
    refetchInterval: REFETCH_MS,
  })

  // 排空标记（FR-10）：用于在表内标 drain 态并切换 drain/undrain 操作。
  const { data: drains } = useQuery({
    queryKey: ['drains', effectiveFilter.namespace],
    queryFn: () => listDrains(effectiveFilter.namespace),
    refetchInterval: REFETCH_MS,
  })

  // 各小区默认入口（FR-48）：供 bungee 详情展示所属小区默认入口；按 (namespace, group, zone) 复合键索引。
  const { data: defaultEntries } = useQuery({
    queryKey: ['default-entries', effectiveFilter.namespace],
    queryFn: () => listDefaultEntries(effectiveFilter.namespace),
    refetchInterval: REFETCH_MS,
  })

  // 现有指派（改派对话框沿用备注，避免改派清空运维填写的备注）。
  const { data: assignments } = useQuery({
    queryKey: ['assignments', effectiveFilter.namespace],
    queryFn: () => listAssignments(effectiveFilter.namespace),
  })

  // 筛选维度下拉的候选来源（FR-51）：大区 / 小区由 zone 汇总与全量实例派生。
  // 候选不随当前过滤收窄（全量拉取），且筛选框允许键入候选外的值（可编辑）。
  const allInstancesQuery = useQuery({
    queryKey: ['instances', 'filter-options'],
    queryFn: () => listInstances({}),
  })
  const zoneSummaryQuery = useQuery({ queryKey: ['zone-summary', 'all'], queryFn: () => zoneSummary() })
  // 环境候选（仅供「新服接入向导」表单的环境下拉，非页内筛选）：候选显示「编码 · 名称」，真实值仍是 code（FR-70）。
  const namespacesQuery = useQuery({ queryKey: ['namespaces'], queryFn: () => listNamespaces() })
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

  // 各环境 agent 多数版本（FR-86）：按当前列出实例聚合，供逐行判定版本是否不一致打黄标。
  const majorityVersions = useMemo(() => buildMajorityVersions(data ?? []), [data])

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

  // 顶部汇总条派生（FR-106）：全部从已拉数据派生，不发新请求。
  // 总实例 / 在线 / 失联 / 排空（drains 数）/ 未分配（assigned=false）。
  const summaryItems = useMemo<SummaryItem[]>(() => {
    const list = data ?? []
    const online = list.filter((i) => i.status === 'online').length
    const lost = list.filter((i) => i.status === 'lost').length
    const unassigned = list.filter((i) => !i.assigned).length
    const drainCount = drains?.length ?? 0
    return [
      { label: t('servers.summaryTotal'), value: list.length },
      { label: t('servers.summaryOnline'), value: online, tone: 'success' },
      { label: t('servers.summaryLost'), value: lost, tone: lost > 0 ? 'danger' : 'default' },
      { label: t('servers.summaryDrained'), value: drainCount, tone: drainCount > 0 ? 'warning' : 'default' },
      { label: t('servers.summaryUnassigned'), value: unassigned, tone: unassigned > 0 ? 'warning' : 'default' },
    ]
  }, [data, drains, t])

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

  // 强制重同步（FR-91）：下发 resync-config 命令，令该 agent 重拉有效配置/文件树/覆盖集并 apply。
  const resyncMut = useMutation({
    mutationFn: ({ serverId, namespace: ns }: { serverId: string; namespace: string }) =>
      triggerResync(serverId, ns),
    onSuccess: (_d, { serverId }) => msg.showSuccess(t('servers.msgResyncTriggered', { serverId })),
    onError: (e: Error) => msg.showError(e.message),
  })

  // 打开详情 Sheet（focusLogs 为 true 时自动触发取日志，供「查看日志」入口直达）。
  function openDetail(i: InstanceView, focusLogs: boolean) {
    setDetailFocusLogs(focusLogs)
    setDetailInstance(i)
  }

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
    // namespace 不在页内筛选；由全局环境合并进 effectiveFilter。
    setFilter({
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
    // 版本/agent 合一列（FR-106）：原 colVersion + colAgentVersion 合并
    { header: t('servers.colVersionAgent'), cell: (i) => versionAgentCell(t, i, majorityVersions) },
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
        // 操作列 stopPropagation：避免触发行点击（打开详情 Sheet）。
        const stop = (e: React.MouseEvent) => e.stopPropagation()
        // 整合为单个「⋯」下拉菜单：含查看类（agent 详情 / 查看日志）+ 运维类（重同步 / drain / 改派 / 下线）。
        // 下线确认弹窗提到菜单外层受控触发（offlineTarget），避免菜单关闭吞掉 AlertDialog。
        return (
          <div onClick={stop}>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="sm" aria-label={t('servers.actionsMenu')}>
                  ⋯
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-44">
                <DropdownMenuItem onClick={() => openDetail(i, false)}>
                  {t('servers.actionAgentDetail')}
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => openDetail(i, true)}>
                  {t('servers.actionViewLogs')}
                </DropdownMenuItem>
                <DropdownMenuItem
                  disabled={resyncMut.isPending}
                  onClick={() => resyncMut.mutate({ serverId: i.serverId, namespace: i.namespace })}
                >
                  {t('servers.actionResync')}
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                {/* drain / undrain（FR-10）：按当前排空态切换 */}
                {drained ? (
                  <DropdownMenuItem
                    onClick={() => undrainMut.mutate({ serverId: i.serverId, namespace: i.namespace })}
                  >
                    {t('servers.undrainBtn')}
                  </DropdownMenuItem>
                ) : (
                  <DropdownMenuItem
                    onClick={() => drainMut.mutate({ serverId: i.serverId, namespace: i.namespace })}
                  >
                    {t('servers.drainBtn')}
                  </DropdownMenuItem>
                )}
                {/* 区改派（FR-71）：仅 bukkit 子服可指派进 zone（BC 代理不可，与后端校验一致 FR-8/FR-35） */}
                {i.role !== ROLE_BUNGEE && (
                  <DropdownMenuItem onClick={() => setReassignTarget(i)}>
                    {t('servers.reassignBtn')}
                  </DropdownMenuItem>
                )}
                <DropdownMenuSeparator />
                {/* 下线（FR-49）：受控弹窗在菜单外层二次确认（FR-76），绝不丢确认 */}
                <DropdownMenuItem
                  className="text-destructive focus:text-destructive"
                  onClick={() => setOfflineTarget(i)}
                >
                  {t('servers.offlineBtn')}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        )
      },
    },
  ]

  // 页眉（FR-105）：标题 + 刷新副标题，新服接入向导入口移入主操作槽（向导开关状态仍在本组件）
  usePageHeader({
    title: t('servers.title'),
    envScoped: true,
    subtitle: isFetching ? t('common.refreshing') : undefined,
    // 新服接入引导向导入口（FR-85）；操作槽已右对齐，去掉原 ml-auto
    actions: <Button onClick={() => setWizardOpen(true)}>{t('servers.wizardOpenBtn')}</Button>,
  })

  return (
    <div className="space-y-4">
      {/* 顶部汇总条（FR-106）：关键计数一排紧凑 metric */}
      <SummaryStrip items={summaryItems} />

      {/* 内联吸顶工具栏（FR-106）：原筛选 Card 压成一行紧凑控件，保留全部筛选维度与「查询」 */}
      <form
        onSubmit={onSearch}
        className="sticky top-0 z-10 flex flex-wrap items-center gap-2 bg-background py-1"
      >
        {/* 环境收口（FR-105 真机打磨）：原页内环境筛选已移除，环境改读页眉全局环境槽。 */}
        <Combobox
          id="f-group"
          aria-label={t('common.group')}
          className="w-36"
          placeholder={t('common.group')}
          value={group}
          onChange={setGroup}
          options={groupOptions}
          allowCustom
        />
        <Combobox
          id="f-zone"
          aria-label={t('common.zone')}
          className="w-36"
          placeholder={t('common.zone')}
          value={zone}
          onChange={setZone}
          options={zoneOptions}
          allowCustom
        />
        <Select value={role} onValueChange={setRole}>
          <SelectTrigger className="w-32" aria-label={t('common.role')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>{t('servers.filterAll')}</SelectItem>
            <SelectItem value="bukkit">bukkit</SelectItem>
            <SelectItem value="bungee">bungee</SelectItem>
          </SelectContent>
        </Select>
        <Select value={status} onValueChange={setStatus}>
          <SelectTrigger className="w-32" aria-label={t('common.status')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>{t('servers.filterAll')}</SelectItem>
            <SelectItem value="online">online</SelectItem>
            <SelectItem value="lost">lost</SelectItem>
            <SelectItem value="offline">offline</SelectItem>
          </SelectContent>
        </Select>
        <Button type="submit">{t('common.query')}</Button>
      </form>

      {/* 裸密表（FR-106）：去 Card 外壳，列多时横向滚动不挤压 */}
      <AsyncSection
        isLoading={isLoading}
        isError={isError}
        error={error}
        skeleton={<TableSkeleton columns={columns.length} />}
      >
        <div className="overflow-x-auto">
          <DataTable
            columns={columns}
            rows={data}
            rowKey={(i) => `${i.namespace}/${i.serverId}`}
            emptyText={t('servers.empty')}
            onRowClick={(i) => openDetail(i, false)}
            rowClassName={(i) => (!i.assigned ? 'bg-amber-50' : undefined)}
          />
        </div>
      </AsyncSection>

      {/* 已主动下线标记（FR-49）：已下线实例不在上表（已移出可用集），裸分区标题 + 表，支持取消下线 */}
      {offlineMarkers && offlineMarkers.length > 0 && (
        <div className="space-y-2">
          <h2 className="text-sm font-semibold text-muted-foreground">{t('servers.offlineSectionTitle')}</h2>
          <div className="overflow-x-auto">
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
          </div>
        </div>
      )}

      {/* 下线二次确认（FR-49/FR-76）：从行操作菜单外层受控触发，避免菜单关闭吞掉弹窗，绝不丢确认 */}
      <AlertDialog open={offlineTarget !== null} onOpenChange={(o) => !o && setOfflineTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {offlineTarget && t('servers.offlineConfirmTitle', { serverId: offlineTarget.serverId })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {offlineTarget && t('servers.offlineConfirmDesc', { namespace: offlineTarget.namespace })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('common.cancel')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (offlineTarget) {
                  offlineMut.mutate({ serverId: offlineTarget.serverId, namespace: offlineTarget.namespace })
                }
                setOfflineTarget(null)
              }}
            >
              {t('servers.offlineConfirmAction')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* 单服详情 Sheet：点行从右侧滑出，按 role 分区展示深指标 + 关系，不发新请求 */}
      <ServerDetailSheet
        instance={detailInstance}
        focusLogs={detailFocusLogs}
        onOpenChange={(open) => !open && setDetailInstance(null)}
        defaultEntry={
          detailInstance && detailInstance.zone
            ? entryByZone.get(`${detailInstance.namespace}/${detailInstance.group}/${detailInstance.zone}`)
            : undefined
        }
        agentVersionMismatch={
          detailInstance ? isAgentVersionMismatch(detailInstance, majorityVersions) : false
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
        namespace={namespace}
        nsOptions={nsOptions}
        groupOptions={groupOptions}
      />
    </div>
  )
}
