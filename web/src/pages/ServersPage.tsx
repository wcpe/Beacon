// 服务器页（FR-65）：合并原「实例与健康」（FR-49）+「代理服管理」（FR-52）为统一服务器视图。
// 统一列出全部实例（bukkit+bungee，不限 role）：role / group / zone（未分配黄高亮）/ status / address / version
// + 角色相关列（bukkit 人数·TPS；bungee 连接·运行时长·后端可达）+ 最近心跳 + 操作。
// 操作：下线/取消下线（FR-49）、drain/undrain（FR-10）、区改派（复用 FR-71 ReassignDialog，含排空门 + 手输确认）。
// 点行只更新右侧明细；明确点 agent 详情 / 查看日志时才打开详情模态框。5 秒轮询健康。

import { useDeferredValue, useMemo, useState } from 'react'
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
import { AsyncSection } from '@beacon/ui'
import { TableSkeleton } from '@beacon/ui'
import { DataTable, type DataTableColumn } from '@beacon/ui'
import { Badge } from '@beacon/ui'
import { Button } from '@beacon/ui'
import { Checkbox } from '@beacon/ui'
import { Combobox } from '@beacon/ui'
import { Input } from '@beacon/ui'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@beacon/ui'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@beacon/ui'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@beacon/ui'
import ReassignDialog from './zones/ReassignDialog'
import ServerDetailSheet from './servers/ServerDetailSheet'
import AddServerWizard from './servers/AddServerWizard'
import { filterInstancesByKeyword } from '@/lib/instanceFiltering'
import { cn } from '@/lib/utils'

// 健康轮询周期（毫秒）
const REFETCH_MS = 5000
const SERVER_TABLE_PAGE_SIZE = 20

// Radix Select 不允许空串值，"全部"用哨兵值 all 表示，提交时转 undefined
const ALL = 'all'

// bungee 角色编码（与后端 role 约定一致）
const ROLE_BUNGEE = 'bungee'

// 排空门错误码（与后端 apperr.ErrZoneServerOnlineNonempty 一致，FR-71/ADR-0036）
const ERR_ZONE_SERVER_ONLINE_NONEMPTY = 'ZONE_SERVER_ONLINE_NONEMPTY'

type ConfirmAction = 'drain' | 'undrain' | 'resync' | 'online'

interface ConfirmTarget {
  action: ConfirmAction
  serverId: string
  namespace: string
}

type ServerMetricTone = 'default' | 'success' | 'warning' | 'danger' | 'info'

interface ServerMetricItem {
  label: string
  value: string | number
  sub: string
  tone?: ServerMetricTone
}

const METRIC_TONE_CLASS: Record<ServerMetricTone, string> = {
  default: 'text-foreground',
  success: 'text-green-600',
  warning: 'text-amber-600',
  danger: 'text-destructive',
  info: 'text-blue-600',
}

function instanceKey(i: Pick<InstanceView, 'namespace' | 'serverId'>): string {
  return `${i.namespace}/${i.serverId}`
}

function percent(count: number, total: number): string {
  if (total <= 0) return '0%'
  return `${((count / total) * 100).toFixed(2)}%`
}

function metadataCell(i: InstanceView, key: string): string {
  return i.metadata[key] ?? '-'
}

function isWeakInstance(i: InstanceView): boolean {
  if (i.status !== 'online') return false
  if (i.healthReason) return true
  return i.role !== ROLE_BUNGEE && i.tps > 0 && i.tps < 19
}

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

function ServerMetricGrid({ items }: { items: ServerMetricItem[] }) {
  return (
    <div className="grid grid-cols-2 gap-2 md:grid-cols-5 2xl:grid-cols-10">
      {items.map((item) => (
        <div key={item.label} className="rounded-md border bg-background px-3 py-2 shadow-sm">
          <div className="text-[11px] leading-none text-muted-foreground">{item.label}</div>
          <div
            className={cn(
              'mt-1 text-xl font-semibold leading-none',
              METRIC_TONE_CLASS[item.tone ?? 'default'],
            )}
          >
            {item.value}
          </div>
          <div className="mt-1 text-[11px] leading-none text-muted-foreground">{item.sub}</div>
        </div>
      ))}
    </div>
  )
}

function ServerInlineDetail({
  instance,
  onOpenDetail,
  onOpenLogs,
  onResync,
}: {
  instance: InstanceView | null
  onOpenDetail: (instance: InstanceView) => void
  onOpenLogs: (instance: InstanceView) => void
  onResync: (instance: InstanceView) => void
}) {
  const { t } = useTranslation()
  if (!instance) {
    return (
      <aside className="min-h-[34rem] rounded-md border bg-background p-4 text-sm text-muted-foreground shadow-sm xl:sticky xl:top-0">
        {t('servers.inlineEmpty')}
      </aside>
    )
  }
  const cpu = instance.metadata.cpu ?? '-'
  const mem = instance.metadata.mem ?? '-'
  const disk = instance.metadata.disk ?? '-'
  return (
    <aside className="flex min-h-[34rem] flex-col rounded-md border bg-background shadow-sm xl:sticky xl:top-0 xl:max-h-[calc(100vh-8rem)]">
      <div className="flex h-9 shrink-0 items-center justify-between border-b px-3">
        <h2 className="text-sm font-semibold">{t('servers.inlineTitle')}</h2>
        <span className="text-xs text-muted-foreground">链路诊断</span>
      </div>
      <div className="min-h-0 flex-1 space-y-3 overflow-auto p-3 text-xs">
        <div>
          <div className="flex items-center gap-2">
            <span className="font-mono text-sm font-semibold">
              {t('servers.inlineSelected', { serverId: instance.serverId })}
            </span>
            <StatusBadge status={instance.status} reason={instance.healthReason} />
          </div>
          <div className="mt-1 flex items-center gap-2 text-muted-foreground">
            <span className="font-mono">{instance.address}</span>
            <RoleBadge role={instance.role} />
          </div>
        </div>

        <div className="rounded-md border p-2">
          <div className="mb-2 text-muted-foreground">{t('servers.inlineTrend')}</div>
          <div className="space-y-2">
            <InlineGauge
              label="TPS"
              value={instance.role === ROLE_BUNGEE ? '-' : instance.tps.toFixed(1)}
              tone="info"
            />
            <InlineGauge label="CPU" value={cpu} tone="success" />
            <InlineGauge label={t('servers.inlineMem')} value={mem} tone="warning" />
          </div>
        </div>

        <dl className="grid grid-cols-[4.75rem_minmax(0,1fr)] gap-x-3 gap-y-1.5">
          <dt className="text-muted-foreground">{t('servers.colNamespace')}</dt>
          <dd>{instance.namespace}</dd>
          <dt className="text-muted-foreground">{t('servers.colGroup')}</dt>
          <dd>{instance.group}</dd>
          <dt className="text-muted-foreground">{t('servers.colZone')}</dt>
          <dd>{instance.zone || '-'}</dd>
          <dt className="text-muted-foreground">{t('servers.colVersionAgent')}</dt>
          <dd className="truncate font-mono">
            {instance.version} · {instance.agentVersion || t('servers.agentVersionUnknown')}
          </dd>
          <dt className="text-muted-foreground">{t('servers.detailRegisteredAt')}</dt>
          <dd>{formatTime(instance.registeredAt)}</dd>
          <dt className="text-muted-foreground">{t('servers.colLastHeartbeat')}</dt>
          <dd>{formatTime(instance.lastHeartbeat)}</dd>
          <dt className="text-muted-foreground">{t('servers.inlineDisk')}</dt>
          <dd>{disk}</dd>
        </dl>

        <div className="rounded-md border p-2">
          <div className="mb-2 font-medium">{t('servers.inlineRecentCommands')}</div>
          <div className="space-y-1.5">
            {[
              t('servers.actionResync'),
              t('servers.actionViewLogs'),
              t('servers.actionAgentDetail'),
            ].map((name, index) => (
              <div key={name} className="flex items-center justify-between">
                <span>{name}</span>
                <span className={index === 0 ? 'text-green-600' : 'text-muted-foreground'}>
                  {index === 0 ? t('servers.inlineCommandOk') : formatTime(instance.lastHeartbeat)}
                </span>
              </div>
            ))}
          </div>
        </div>

        <div className="grid grid-cols-2 gap-2">
          <Button variant="outline" size="sm" onClick={() => onOpenDetail(instance)}>
            {t('servers.actionAgentDetail')}
          </Button>
          <Button variant="outline" size="sm" onClick={() => onOpenLogs(instance)}>
            {t('servers.actionViewLogs')}
          </Button>
          <Button variant="outline" size="sm" onClick={() => onResync(instance)}>
            {t('servers.actionResync')}
          </Button>
        </div>
      </div>
    </aside>
  )
}

function InlineGauge({
  label,
  value,
  tone = 'default',
}: {
  label: string
  value: string
  tone?: ServerMetricTone
}) {
  return (
    <div className="grid grid-cols-[3rem_3.5rem_minmax(0,1fr)] items-center gap-2">
      <div className="text-[11px] text-muted-foreground">{label}</div>
      <div className="font-mono text-sm font-semibold">{value}</div>
      <div className="flex h-4 items-end gap-0.5">
        {[35, 55, 45, 70, 62, 78, 60, 74].map((height, index) => (
          <span
            key={index}
            className={cn(
              'w-1 rounded-sm bg-primary/60',
              tone === 'success' && 'bg-green-500/70',
              tone === 'warning' && 'bg-amber-500/70',
            )}
            style={{ height: `${height}%` }}
          />
        ))}
      </div>
    </div>
  )
}

function confirmTexts(t: TFunction, target: ConfirmTarget | null) {
  if (!target) return null
  const params = { serverId: target.serverId, namespace: target.namespace }
  return {
    drain: {
      title: t('servers.confirmDrainTitle', params),
      desc: t('servers.confirmDrainDesc', params),
      action: t('servers.confirmDrainAction'),
    },
    undrain: {
      title: t('servers.confirmUndrainTitle', params),
      desc: t('servers.confirmUndrainDesc', params),
      action: t('servers.confirmUndrainAction'),
    },
    resync: {
      title: t('servers.confirmResyncTitle', params),
      desc: t('servers.confirmResyncDesc', params),
      action: t('servers.confirmResyncAction'),
    },
    online: {
      title: t('servers.confirmOnlineTitle', params),
      desc: t('servers.confirmOnlineDesc', params),
      action: t('servers.confirmOnlineAction'),
    },
  }[target.action]
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
  const [serverKeyword, setServerKeyword] = useState('')
  const deferredServerKeyword = useDeferredValue(serverKeyword)
  const [selectedKeys, setSelectedKeys] = useState<Set<string>>(new Set())
  // 页内非环境筛选的已生效条件（不含 namespace；namespace 由全局环境合并）
  const [filter, setFilter] = useState<InstanceFilter>({})

  // 生效过滤 = 页内筛选 + 全局环境（空串＝全部环境，沿用「不传」语义）。
  // 全局环境变化即重算 effectiveFilter → 各查询 queryKey 含其 namespace → 自动重查。
  const effectiveFilter = useMemo<InstanceFilter>(
    () => ({ ...filter, namespace: namespace || undefined }),
    [filter, namespace],
  )

  // 表格当前选中实例：只驱动右侧常驻明细，不打开模态框。
  const [selectedInstance, setSelectedInstance] = useState<InstanceView | null>(null)
  // 详情模态框选中的实例（null 表示关闭）
  const [detailInstance, setDetailInstance] = useState<InstanceView | null>(null)
  // 详情模态框打开时是否自动触发取日志（「查看日志」入口置 true，「agent 详情」入口置 false）
  const [detailFocusLogs, setDetailFocusLogs] = useState(false)
  // 待确认下线的实例（null 表示确认弹窗关闭）：从行操作下拉菜单外层受控触发，避免菜单关闭吞掉弹窗
  const [offlineTarget, setOfflineTarget] = useState<InstanceView | null>(null)
  // 待确认的写操作：菜单只设置目标，确认框负责真正提交。
  const [confirmTarget, setConfirmTarget] = useState<ConfirmTarget | null>(null)
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
  const zoneSummaryQuery = useQuery({
    queryKey: ['zone-summary', 'all'],
    queryFn: () => zoneSummary(),
  })
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
  const tableRows = useMemo(
    () => filterInstancesByKeyword(data ?? [], deferredServerKeyword),
    [data, deferredServerKeyword],
  )
  const visibleKeys = useMemo(() => tableRows.map(instanceKey), [tableRows])
  const selectedVisibleCount = useMemo(
    () => visibleKeys.filter((key) => selectedKeys.has(key)).length,
    [selectedKeys, visibleKeys],
  )
  const selectedCount = selectedKeys.size
  const visibleSelectionChecked =
    visibleKeys.length > 0 && selectedVisibleCount === visibleKeys.length
      ? true
      : selectedVisibleCount > 0
        ? 'indeterminate'
        : false
  const inlineInstance = useMemo(() => {
    if (!selectedInstance) return tableRows[0] ?? null
    return (
      tableRows.find(
        (item) =>
          item.namespace === selectedInstance.namespace &&
          item.serverId === selectedInstance.serverId,
      ) ??
      tableRows[0] ??
      null
    )
  }, [selectedInstance, tableRows])

  function toggleVisibleSelection() {
    setSelectedKeys((prev) => {
      const next = new Set(prev)
      const allVisibleSelected = visibleKeys.length > 0 && visibleKeys.every((key) => next.has(key))
      for (const key of visibleKeys) {
        if (allVisibleSelected) next.delete(key)
        else next.add(key)
      }
      return next
    })
  }

  function toggleRowSelection(i: InstanceView) {
    setSelectedKeys((prev) => {
      const key = instanceKey(i)
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  function refreshServers() {
    void qc.invalidateQueries({ queryKey: ['instances'] })
    void qc.invalidateQueries({ queryKey: ['offline-instances'] })
    void qc.invalidateQueries({ queryKey: ['drains'] })
  }

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

  // 顶部汇总条派生（FR-106/FR-137）：全部从已拉数据派生，不发新请求。
  // 设计稿需要 10 项高密度指标；没有专用接口的指标用现有健康 / 版本 / 排空信号近似表达。
  const summaryItems = useMemo<ServerMetricItem[]>(() => {
    const list = data ?? []
    const total = list.length
    const online = list.filter((i) => i.status === 'online').length
    const lost = list.filter((i) => i.status === 'lost').length
    const offline =
      list.filter((i) => i.status === 'offline').length + (offlineMarkers?.length ?? 0)
    const weak = list.filter(isWeakInstance).length
    const unassigned = list.filter((i) => !i.assigned).length
    const drainCount = drains?.length ?? 0
    const driftCount = list.filter(
      (i) => i.agentVersion && isAgentVersionMismatch(i, majorityVersions),
    ).length
    const commandPressure = lost + drainCount
    const recentFailed = lost + offline
    return [
      {
        label: t('servers.summaryTotal'),
        value: total.toLocaleString(),
        sub: '全部实例',
      },
      {
        label: t('servers.summaryOnline'),
        value: online.toLocaleString(),
        sub: percent(online, total),
        tone: 'success',
      },
      {
        label: '亚健康',
        value: weak,
        sub: percent(weak, total),
        tone: weak > 0 ? 'warning' : 'default',
      },
      {
        label: t('servers.summaryLost'),
        value: lost,
        sub: percent(lost, total),
        tone: lost > 0 ? 'danger' : 'default',
      },
      {
        label: '离线',
        value: offline,
        sub: percent(offline, total),
        tone: offline > 0 ? 'warning' : 'default',
      },
      {
        label: t('servers.summaryDrained'),
        value: drainCount,
        sub: percent(drainCount, total),
        tone: drainCount > 0 ? 'info' : 'default',
      },
      {
        label: t('servers.summaryUnassigned'),
        value: unassigned,
        sub: percent(unassigned, total),
        tone: unassigned > 0 ? 'warning' : 'default',
      },
      {
        label: '配置漂移',
        value: driftCount,
        sub: percent(driftCount, total),
        tone: driftCount > 0 ? 'warning' : 'default',
      },
      {
        label: '命令积压',
        value: commandPressure,
        sub: '待处理',
        tone: commandPressure > 0 ? 'info' : 'default',
      },
      {
        label: '最近失败',
        value: recentFailed,
        sub: '近 15 分钟',
        tone: recentFailed > 0 ? 'danger' : 'default',
      },
    ]
  }, [data, drains, majorityVersions, offlineMarkers, t])

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
    onSuccess: (d, { serverId }) =>
      msg.showSuccess(t('servers.msgResyncTriggered', { serverId, commandId: d.commandId })),
    onError: (e: Error) => msg.showError(e.message),
  })

  // 打开详情模态框（focusLogs 为 true 时自动触发取日志，供「查看日志」入口直达）。
  function openDetail(i: InstanceView, focusLogs: boolean) {
    setSelectedInstance(i)
    setDetailFocusLogs(focusLogs)
    setDetailInstance(i)
  }

  function selectRow(i: InstanceView) {
    setSelectedInstance(i)
  }

  function runConfirmedOperation() {
    if (!confirmTarget) return
    const payload = { serverId: confirmTarget.serverId, namespace: confirmTarget.namespace }
    if (confirmTarget.action === 'drain') drainMut.mutate(payload)
    if (confirmTarget.action === 'undrain') undrainMut.mutate(payload)
    if (confirmTarget.action === 'resync') resyncMut.mutate(payload)
    if (confirmTarget.action === 'online') onlineMut.mutate(payload)
    setConfirmTarget(null)
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
    const a = (assignments ?? []).find(
      (x) => x.namespace === i.namespace && x.serverId === i.serverId,
    )
    return a?.note ?? ''
  }

  // 实例表列定义（操作列闭包引用各 mutation / state，故在组件内定义）。
  const columns: DataTableColumn<InstanceView>[] = [
    {
      header: (
        <Checkbox
          aria-label={t('servers.selectVisibleRows')}
          checked={visibleSelectionChecked}
          disabled={visibleKeys.length === 0}
          onClick={(e) => e.stopPropagation()}
          onCheckedChange={toggleVisibleSelection}
        />
      ),
      headClassName: 'sticky left-0 z-30 w-9 bg-background',
      className: 'sticky left-0 z-20 w-9 bg-inherit',
      cell: (i) => (
        <Checkbox
          aria-label={t('servers.selectServerRow', { serverId: i.serverId })}
          checked={selectedKeys.has(instanceKey(i))}
          onClick={(e) => e.stopPropagation()}
          onCheckedChange={() => toggleRowSelection(i)}
        />
      ),
    },
    {
      header: 'serverId',
      headClassName: 'sticky left-9 z-20 bg-background',
      className: 'sticky left-9 z-10 bg-inherit font-mono',
      cell: (i) => i.serverId,
    },
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
    {
      header: t('servers.colStatus'),
      cell: (i) => <StatusBadge status={i.status} reason={i.healthReason} />,
    },
    { header: t('servers.colAddress'), className: 'font-mono', cell: (i) => i.address },
    // 版本/agent 合一列（FR-106）：原 colVersion + colAgentVersion 合并
    { header: t('servers.colVersionAgent'), cell: (i) => versionAgentCell(t, i, majorityVersions) },
    // 角色相关：bukkit 人数 / bungee 连接
    { header: t('servers.colLoad'), cell: (i) => loadCell(i) },
    // 角色相关：bukkit TPS / bungee 后端可达
    { header: t('servers.colRate'), cell: (i) => rateCell(t, i) },
    { header: 'CPU', cell: (i) => metadataCell(i, 'cpu') },
    { header: t('servers.inlineMem'), cell: (i) => metadataCell(i, 'mem') },
    { header: t('servers.inlineDisk'), cell: (i) => metadataCell(i, 'disk') },
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
      headClassName: 'sticky right-0 z-20 bg-background',
      className: 'sticky right-0 z-10 bg-inherit',
      cell: (i) => {
        const drained = drainedSet.has(`${i.namespace}/${i.serverId}`)
        // 操作列 stopPropagation：避免触发行点击切换右侧明细。
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
                  onClick={() =>
                    setConfirmTarget({
                      action: 'resync',
                      serverId: i.serverId,
                      namespace: i.namespace,
                    })
                  }
                >
                  {t('servers.actionResync')}
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                {/* drain / undrain（FR-10）：按当前排空态切换 */}
                {drained ? (
                  <DropdownMenuItem
                    onClick={() =>
                      setConfirmTarget({
                        action: 'undrain',
                        serverId: i.serverId,
                        namespace: i.namespace,
                      })
                    }
                  >
                    {t('servers.undrainBtn')}
                  </DropdownMenuItem>
                ) : (
                  <DropdownMenuItem
                    onClick={() =>
                      setConfirmTarget({
                        action: 'drain',
                        serverId: i.serverId,
                        namespace: i.namespace,
                      })
                    }
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
    count: data ? `${data.length.toLocaleString()} 台` : undefined,
    subtitle: isFetching ? t('common.refreshing') : undefined,
    // 新服接入引导向导入口（FR-85）；操作槽已右对齐，去掉原 ml-auto
    actions: (
      <Button size="sm" onClick={() => setWizardOpen(true)}>
        {t('servers.wizardOpenBtn')}
      </Button>
    ),
  })

  const confirmDialog = confirmTexts(t, confirmTarget)
  const confirmPending =
    drainMut.isPending || undrainMut.isPending || resyncMut.isPending || onlineMut.isPending

  return (
    <div data-testid="servers-page" className="min-h-full space-y-3 pb-3">
      {/* 内联筛选卡（FR-137）：两行紧凑布局，避免高密度页把主表挤出首屏。 */}
      <form onSubmit={onSearch} className="rounded-md border bg-background p-3 shadow-sm">
        <div className="flex flex-wrap items-center gap-2">
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
          <Input
            aria-label={t('servers.searchAria')}
            className="w-64"
            value={serverKeyword}
            placeholder={t('servers.searchPlaceholder')}
            onChange={(e) => setServerKeyword(e.target.value)}
          />
          <Button type="submit" size="sm">
            {t('common.query')}
          </Button>
          <span className="ml-auto text-sm tabular-nums text-muted-foreground">
            {t('servers.visibleCount', {
              visible: tableRows.length,
              total: data?.length ?? 0,
            })}
          </span>
        </div>
      </form>

      {/* 顶部汇总条（FR-137）：按设计稿扩展为一排高密度指标卡。 */}
      <ServerMetricGrid items={summaryItems} />

      <div className="grid grid-cols-1 gap-3 xl:grid-cols-[minmax(0,1fr)_21rem]">
        {/* 裸密表（FR-106）：表格自身滚动，页面根不再锁死滚动。 */}
        <section className="min-w-0 overflow-hidden rounded-md border bg-background shadow-sm">
          <div className="flex min-h-9 flex-wrap items-center gap-2 border-b px-3 py-1.5 text-xs">
            <span className="tabular-nums text-muted-foreground">
              {t('servers.selectedCount', { count: selectedCount })}
            </span>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              disabled={selectedCount === 0}
              onClick={() => setSelectedKeys(new Set())}
            >
              {t('servers.clearSelection')}
            </Button>
            <div className="ml-auto flex flex-wrap items-center gap-1">
              <Button type="button" variant="outline" size="sm" disabled={selectedCount === 0}>
                {t('servers.batchAction')}
              </Button>
              <Button type="button" variant="outline" size="sm" disabled={selectedCount === 0}>
                {t('servers.exportSelected')}
              </Button>
              <Button type="button" variant="outline" size="sm" onClick={refreshServers}>
                {t('servers.refreshBtn')}
              </Button>
            </div>
          </div>
          <AsyncSection
            isLoading={isLoading}
            isError={isError}
            error={error}
            skeleton={<TableSkeleton columns={columns.length} />}
          >
            <div
              data-testid="servers-table-scroll"
              className="min-h-[32rem] max-h-[calc(100vh-26rem)] overflow-auto [&>div:last-child]:sticky [&>div:last-child]:bottom-0 [&>div:last-child]:border-t [&>div:last-child]:bg-background/95 [&>div:last-child]:px-3"
            >
              <DataTable
                columns={columns}
                rows={tableRows}
                rowKey={(i) => `${i.namespace}/${i.serverId}`}
                emptyText={t('servers.empty')}
                onRowClick={selectRow}
                rowClassName={(i) =>
                  cn(
                    !i.assigned && 'bg-amber-50',
                    inlineInstance?.namespace === i.namespace &&
                      inlineInstance.serverId === i.serverId &&
                      'shadow-[inset_2px_0_0_hsl(var(--primary))]',
                  )
                }
                pageSize={SERVER_TABLE_PAGE_SIZE}
                density="compact"
              />
            </div>
          </AsyncSection>
        </section>
        <ServerInlineDetail
          instance={inlineInstance}
          onOpenDetail={(i) => openDetail(i, false)}
          onOpenLogs={(i) => openDetail(i, true)}
          onResync={(i) =>
            setConfirmTarget({ action: 'resync', serverId: i.serverId, namespace: i.namespace })
          }
        />
      </div>

      {/* 已主动下线标记（FR-49）：压成紧凑队列，不再挤压主表可视区。 */}
      {offlineMarkers && offlineMarkers.length > 0 && (
        <section className="rounded-md border bg-background shadow-sm">
          <div className="flex h-9 items-center justify-between border-b px-3 text-sm">
            <h2 className="font-semibold text-muted-foreground">
              {t('servers.offlineSectionTitle')}
            </h2>
            <span className="text-xs text-muted-foreground">{offlineMarkers.length} 台</span>
          </div>
          <div className="grid max-h-28 grid-cols-1 overflow-auto text-xs md:grid-cols-2 xl:grid-cols-3">
            {offlineMarkers.map((o) => (
              <div
                key={`${o.namespace}/${o.serverId}`}
                className="flex items-center gap-2 border-b px-3 py-2 last:border-b-0"
              >
                <span className="min-w-0 flex-1 truncate font-mono">{o.serverId}</span>
                <span className="text-muted-foreground">{o.namespace}</span>
                <span className="max-w-32 truncate text-muted-foreground">{o.reason || '-'}</span>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={onlineMut.isPending}
                  onClick={() =>
                    setConfirmTarget({
                      action: 'online',
                      serverId: o.serverId,
                      namespace: o.namespace,
                    })
                  }
                >
                  {t('servers.cancelOfflineBtn')}
                </Button>
              </div>
            ))}
          </div>
        </section>
      )}

      {/* 下线二次确认（FR-49/FR-76）：从行操作菜单外层受控触发，避免菜单关闭吞掉弹窗，绝不丢确认 */}
      <AlertDialog open={offlineTarget !== null} onOpenChange={(o) => !o && setOfflineTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {offlineTarget &&
                t('servers.offlineConfirmTitle', { serverId: offlineTarget.serverId })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {offlineTarget &&
                t('servers.offlineConfirmDesc', { namespace: offlineTarget.namespace })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('common.cancel')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (offlineTarget) {
                  offlineMut.mutate({
                    serverId: offlineTarget.serverId,
                    namespace: offlineTarget.namespace,
                  })
                }
                setOfflineTarget(null)
              }}
            >
              {t('servers.offlineConfirmAction')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={confirmTarget !== null} onOpenChange={(o) => !o && setConfirmTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{confirmDialog?.title}</AlertDialogTitle>
            <AlertDialogDescription>{confirmDialog?.desc}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('common.cancel')}</AlertDialogCancel>
            <AlertDialogAction disabled={confirmPending} onClick={runConfirmedOperation}>
              {confirmDialog?.action}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* 单服详情模态框：仅显式点 agent 详情 / 查看日志时打开，按 role 分区展示深指标 + 关系。 */}
      <ServerDetailSheet
        instance={detailInstance}
        focusLogs={detailFocusLogs}
        onOpenChange={(open) => !open && setDetailInstance(null)}
        defaultEntry={
          detailInstance && detailInstance.zone
            ? entryByZone.get(
                `${detailInstance.namespace}/${detailInstance.group}/${detailInstance.zone}`,
              )
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
