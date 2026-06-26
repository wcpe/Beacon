// 命令观测 / 审查页（FR-104，增强 FR-17/FR-82）：观测控制面↔agent 控制命令（agent_command：
// ingest-plugins / tail-logs / resync-config / 反向抓取）的双向生命周期（下发 pending → agent 拉取 fetched →
// 回执 done/failed/expired）。含 KPI（总数 / 按状态 / 按类型）+ 实时队列逐条（自动刷新）+ 历史可过滤查询 + 命令量趋势。
// 吸收原「控制面健康逐条队列明细」诉求：把 FR-82 的命令队列计数升级为逐条 + 已等时长。
// 只读：复用既有看板范式（环境 Combobox + 时间窗 Tabs + StatCard/IconStat + DataTable + recharts），不引新依赖。
// 边界：与 FR-73 服务分析（聚合 admin 操作审计）/ 审计日志（人的操作流水）数据源 / 页面独立——本页是「命令在做什么」。

import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { Activity, CheckCircle2, Clock, ListChecks, Loader2, Server, Terminal, XCircle } from 'lucide-react'
import { getCommandAnalytics, listCommands, listNamespaces } from '../api/client'
import type { CommandFilter } from '../api/client'
import type { CommandMetaView } from '../api/types'
import { formatDuration, formatTime, namespaceOptions } from '../api/format'
import { usePageHeader } from '@/components/PageHeader'
import StatCard from './dashboard/StatCard'
import CommandTrendChart from './command-observability/CommandTrendChart'
import IconStat from '@/components/dashboard/IconStat'
import { levelSoft, type HealthLevel } from '@/components/dashboard/health'
import AsyncSection from '@/components/AsyncSection'
import { CardGridSkeleton, TableSkeleton } from '@/components/skeletons'
import { Skeleton } from '@/components/ui/skeleton'
import DataTable, { type DataTableColumn } from '@/components/DataTable'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Combobox } from '@/components/ui/combobox'
import { Card, CardContent } from '@/components/ui/card'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

// 单页条数（固定，运维场景无需可配）
const PAGE_SIZE = 20
// 实时队列自动刷新节奏（毫秒）：与既有页轮询节奏一致，管理台无浏览器 SSE，沿用轮询
const QUEUE_REFETCH_MS = 5000
// 过滤下拉「全部」哨兵值（Select 不支持空串选项，映射回 undefined）
const ALL = 'all'
// 命令类型枚举（与后端 model.CommandType* 一致）
const TYPE_OPTIONS = ['ingest-plugins', 'tail-logs', 'resync-config']
// 命令状态枚举（与后端 model.CommandStatus* 一致）
const STATUS_OPTIONS = ['pending', 'fetched', 'ready', 'done', 'failed', 'expired']
// 实时队列过滤的状态集合（待拉取 + 执行中）
const QUEUE_STATUSES = ['pending', 'fetched']

// 时间窗预设（天数）：本地算 from/to 传 RFC3339；窗口上限 92 天内，故 7/30 天均合法。
type AnalyticsWindow = '7d' | '30d'
const WINDOWS: Array<{ value: AnalyticsWindow; days: number; labelKey: string }> = [
  { value: '7d', days: 7, labelKey: 'commandObs.win7d' },
  { value: '30d', days: 30, labelKey: 'commandObs.win30d' },
]
const DEFAULT_WINDOW: AnalyticsWindow = '30d'
const MS_PER_DAY = 24 * 60 * 60 * 1000

// 由窗口天数算 [from, to] 的 RFC3339 字符串（to=当前，from=to-days 天）。
function windowRange(days: number): { from: string; to: string } {
  const now = Date.now()
  return { from: new Date(now - days * MS_PER_DAY).toISOString(), to: new Date(now).toISOString() }
}

// 把表单本地时间值转成后端可识别的 ISO（UTC）字符串；空值返回 undefined。
function toIso(local: string): string | undefined {
  if (!local) return undefined
  const d = new Date(local)
  if (Number.isNaN(d.getTime())) return undefined
  return d.toISOString()
}

// 命令状态 → 健康等级（KPI 数值与状态徽标上色）：失败 / 过期=danger、执行中 / 待拉取 / 待确认=warn、完成=ok。
function statusLevel(status: string): HealthLevel {
  if (status === 'failed' || status === 'expired') return 'danger'
  if (status === 'done') return 'ok'
  if (status === 'pending' || status === 'fetched' || status === 'ready') return 'warn'
  return 'muted'
}

export default function CommandObservabilityPage() {
  const { t } = useTranslation()
  // 命令类型 / 状态英文枚举 → 中文（未知经 defaultValue 回退原文，后端仍返英文枚举）
  const typeLabel = (type: string) => t(`commandObs.type.${type}`, { defaultValue: type })
  const statusLabel = (status: string) => t(`commandObs.status.${status}`, { defaultValue: status })

  // 环境过滤（可编辑下拉，FR-51）：空表示聚合全部环境；KPI / 趋势 / 实时队列 / 历史均受其影响。
  const [namespace, setNamespace] = useState('')
  const [window, setWindow] = useState<AnalyticsWindow>(DEFAULT_WINDOW)

  // 历史查询过滤草稿（点「查询」才生效）
  const [serverId, setServerId] = useState('')
  const [type, setType] = useState(ALL)
  const [status, setStatus] = useState(ALL)
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  // 历史查询已生效条件
  const [historyFilter, setHistoryFilter] = useState<CommandFilter>({ page: 1, size: PAGE_SIZE })

  // 环境下拉候选来自 listNamespaces；候选显示「编码 · 名称」，真实值仍是 code（FR-70）。
  const namespacesQuery = useQuery({ queryKey: ['namespaces'], queryFn: () => listNamespaces() })
  const nsOptions = useMemo(() => namespaceOptions(namespacesQuery.data), [namespacesQuery.data])

  const days = WINDOWS.find((w) => w.value === window)?.days ?? 30
  const range = useMemo(() => windowRange(days), [days])

  // 聚合（KPI + 趋势 + 按类型 / 服务器）：随环境 / 时间窗变更重查。
  const analyticsQuery = useQuery({
    queryKey: ['command-analytics', namespace, range.from, range.to],
    queryFn: () => getCommandAnalytics({ namespace: namespace || undefined, from: range.from, to: range.to }),
  })

  // 实时队列（pending + fetched）：两次请求各取一状态合并（端点单状态过滤），自动刷新。
  const queueQuery = useQuery({
    queryKey: ['command-queue', namespace],
    queryFn: async () => {
      const pages = await Promise.all(
        QUEUE_STATUSES.map((s) =>
          listCommands({ namespace: namespace || undefined, status: s, page: 1, size: 100 }),
        ),
      )
      // 合并两状态后按创建时间倒序（新建在前）
      return pages
        .flatMap((p) => p.items)
        .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
    },
    refetchInterval: QUEUE_REFETCH_MS,
  })

  // 历史查询（已生效过滤 + 分页）
  const historyQuery = useQuery({
    queryKey: ['command-history', historyFilter],
    queryFn: () => listCommands(historyFilter),
    placeholderData: keepPreviousData,
  })

  const data = analyticsQuery.data
  const total = data?.total ?? 0
  // 按状态计数映射为 map，缺省状态显 0。
  const statusCount = useMemo(() => {
    const m: Record<string, number> = {}
    for (const c of data?.byStatus ?? []) m[c.status] = c.count
    return m
  }, [data])

  function onSearch(e: React.FormEvent) {
    e.preventDefault()
    setHistoryFilter({
      namespace: namespace || undefined,
      serverId: serverId.trim() || undefined,
      type: type === ALL ? undefined : type,
      status: status === ALL ? undefined : status,
      from: toIso(from),
      to: toIso(to),
      page: 1,
      size: PAGE_SIZE,
    })
  }

  function goPage(page: number) {
    setHistoryFilter((f) => ({ ...f, page }))
  }

  const historyTotal = historyQuery.data?.total ?? 0
  const page = historyFilter.page ?? 1
  const totalPages = Math.max(1, Math.ceil(historyTotal / PAGE_SIZE))

  // KPI 卡片下的「按状态」IconStat 组（待拉取 / 执行中 / 已完成 / 失败 / 过期，含健康色）。
  const statusKpis: Array<{ status: string; icon: React.ReactNode }> = [
    { status: 'pending', icon: <Clock className="size-4" /> },
    { status: 'fetched', icon: <Loader2 className="size-4" /> },
    { status: 'done', icon: <CheckCircle2 className="size-4" /> },
    { status: 'failed', icon: <XCircle className="size-4" /> },
    { status: 'expired', icon: <XCircle className="size-4" /> },
  ]

  // 状态徽标：浅底 + 同色字（健康色），中文标签。
  const statusBadge = (s: string) => (
    <span className={`inline-flex rounded px-1.5 py-0.5 text-xs ${levelSoft(statusLevel(s))}`}>
      {statusLabel(s)}
    </span>
  )

  // 历史表列定义
  const columns: DataTableColumn<CommandMetaView>[] = [
    { header: t('commandObs.colCreatedAt'), cell: (c) => formatTime(c.createdAt) },
    { header: t('commandObs.colCommandId'), className: 'font-mono', cell: (c) => c.commandId },
    { header: t('commandObs.colServerId'), cell: (c) => c.serverId },
    { header: t('commandObs.colType'), cell: (c) => typeLabel(c.type) },
    { header: t('commandObs.colStatus'), cell: (c) => statusBadge(c.status) },
    { header: t('commandObs.colOperator'), cell: (c) => c.operator || '-' },
    { header: t('commandObs.colResultDetail'), className: 'max-w-xs truncate', cell: (c) => c.resultDetail || '-' },
  ]

  // 实时队列表列：commandId / 实例 / 类型 / 状态 / 已等时长（客户端按 createdAt 算）/ operator。
  const queueColumns: DataTableColumn<CommandMetaView>[] = [
    { header: t('commandObs.colCommandId'), className: 'font-mono', cell: (c) => c.commandId },
    { header: t('commandObs.colServerId'), cell: (c) => c.serverId },
    { header: t('commandObs.colType'), cell: (c) => typeLabel(c.type) },
    { header: t('commandObs.colStatus'), cell: (c) => statusBadge(c.status) },
    { header: t('commandObs.colAge'), cell: (c) => formatDuration(ageSeconds(c)) },
    { header: t('commandObs.colOperator'), cell: (c) => c.operator || '-' },
  ]

  // 第二层页眉：标题 + 刷新中副标题；本页为环境范围页
  usePageHeader({
    title: t('commandObs.title'),
    subtitle: analyticsQuery.isFetching ? t('common.refreshing') : undefined,
    envScoped: true,
  })

  return (
    <div className="space-y-4">
      <p className="text-sm text-muted-foreground">{t('commandObs.subtitle')}</p>

      {/* 环境 + 时间窗筛选条（作用于 KPI / 趋势 / 实时队列） */}
      <Card>
        <CardContent>
          <div className="flex flex-wrap items-end justify-between gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="co-namespace">{t('common.namespace')}</Label>
              <Combobox
                id="co-namespace"
                aria-label={t('common.namespace')}
                className="w-40"
                placeholder={t('commandObs.nsPlaceholder')}
                value={namespace}
                onChange={setNamespace}
                options={nsOptions}
                allowCustom
                clearable
                clearLabel={t('commandObs.clearFilter')}
              />
            </div>
            <Tabs value={window} onValueChange={(v) => setWindow(v as AnalyticsWindow)}>
              <TabsList>
                {WINDOWS.map((w) => (
                  <TabsTrigger key={w.value} value={w.value}>
                    {t(w.labelKey)}
                  </TabsTrigger>
                ))}
              </TabsList>
            </Tabs>
          </div>
        </CardContent>
      </Card>

      {/* KPI 区：总数 + 按状态 IconStat 组 + 按类型 */}
      <AsyncSection
        isLoading={analyticsQuery.isLoading}
        isError={analyticsQuery.isError}
        error={analyticsQuery.error}
        skeleton={
          <div className="space-y-4">
            <CardGridSkeleton count={4} />
            <Skeleton className="h-64 w-full rounded-xl" />
          </div>
        }
      >
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
          <StatCard label={t('commandObs.cardTotal')} value={total} icon={<Terminal className="size-4" />} />
          {/* 按状态紧凑 KPI 组：一张卡内并排 IconStat（含健康色） */}
          <Card size="sm" className="sm:col-span-2">
            <CardContent>
              <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 xl:grid-cols-5">
                {statusKpis.map((k) => (
                  <IconStat
                    key={k.status}
                    icon={k.icon}
                    label={statusLabel(k.status)}
                    value={statusCount[k.status] ?? 0}
                    level={statusLevel(k.status)}
                  />
                ))}
              </div>
            </CardContent>
          </Card>
        </div>

        <div className="mt-4 grid grid-cols-1 gap-4 xl:grid-cols-2">
          {/* 命令量趋势（下发 / 完成 / 失败三折线） */}
          <Card>
            <CardContent className="space-y-3">
              <div className="flex items-center gap-2 text-base font-medium">
                <Activity className="size-4 text-muted-foreground" />
                {t('commandObs.trendTitle')}
              </div>
              <CommandTrendChart points={data?.byDay ?? []} />
            </CardContent>
          </Card>

          {/* 按类型 + 按服务器分布（计数条） */}
          <Card>
            <CardContent className="space-y-4">
              <div>
                <div className="flex items-center gap-2 text-base font-medium">
                  <ListChecks className="size-4 text-muted-foreground" />
                  {t('commandObs.byTypeTitle')}
                </div>
                <ul className="mt-2 space-y-1.5 text-sm">
                  {(data?.byType ?? []).map((b) => (
                    <li key={b.type} className="flex items-center justify-between">
                      <span>{typeLabel(b.type)}</span>
                      <span className="tabular-nums text-muted-foreground">{b.count}</span>
                    </li>
                  ))}
                  {(data?.byType?.length ?? 0) === 0 && (
                    <li className="text-muted-foreground">{t('commandObs.byServerEmpty')}</li>
                  )}
                </ul>
              </div>
              <div>
                <div className="flex items-center gap-2 text-base font-medium">
                  <Server className="size-4 text-muted-foreground" />
                  {t('commandObs.byServerTitle')}
                </div>
                <ul className="mt-2 space-y-1.5 text-sm">
                  {(data?.byServer ?? []).map((b) => (
                    <li key={b.serverId} className="flex items-center justify-between">
                      <span className="truncate">{b.serverId}</span>
                      <span className="tabular-nums text-muted-foreground">{b.count}</span>
                    </li>
                  ))}
                  {(data?.byServer?.length ?? 0) === 0 && (
                    <li className="text-muted-foreground">{t('commandObs.byServerEmpty')}</li>
                  )}
                </ul>
              </div>
            </CardContent>
          </Card>
        </div>
      </AsyncSection>

      {/* 实时队列（pending + fetched，自动刷新逐条） */}
      <Card>
        <CardContent className="space-y-3">
          <div>
            <div className="flex items-center gap-2 text-base font-medium">
              <Clock className="size-4 text-muted-foreground" />
              {t('commandObs.queueTitle')}
            </div>
            <p className="text-xs text-muted-foreground">{t('commandObs.queueSubtitle')}</p>
          </div>
          <AsyncSection
            isLoading={queueQuery.isLoading}
            isError={queueQuery.isError}
            error={queueQuery.error}
            skeleton={<TableSkeleton columns={queueColumns.length} rows={4} />}
          >
            <DataTable
              columns={queueColumns}
              rows={queueQuery.data}
              rowKey={(c) => String(c.commandId)}
              emptyText={t('commandObs.queueEmpty')}
            />
          </AsyncSection>
        </CardContent>
      </Card>

      {/* 历史查询（过滤 + 分页 + 结果摘要） */}
      <Card>
        <CardContent className="space-y-4">
          <div className="flex items-center gap-2 text-base font-medium">
            <Terminal className="size-4 text-muted-foreground" />
            {t('commandObs.historyTitle')}
          </div>
          <form onSubmit={onSearch} className="flex flex-wrap items-end gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="co-serverid">{t('commandObs.colServerId')}</Label>
              <Input
                id="co-serverid"
                value={serverId}
                onChange={(e) => setServerId(e.target.value)}
                placeholder={t('commandObs.serverIdPlaceholder')}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="co-type">{t('commandObs.colType')}</Label>
              <Select value={type} onValueChange={setType}>
                <SelectTrigger id="co-type" className="w-40">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ALL}>{t('commandObs.typeAll')}</SelectItem>
                  {TYPE_OPTIONS.map((tp) => (
                    <SelectItem key={tp} value={tp}>
                      {typeLabel(tp)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="co-status">{t('commandObs.colStatus')}</Label>
              <Select value={status} onValueChange={setStatus}>
                <SelectTrigger id="co-status" className="w-40">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ALL}>{t('commandObs.statusAll')}</SelectItem>
                  {STATUS_OPTIONS.map((s) => (
                    <SelectItem key={s} value={s}>
                      {statusLabel(s)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="co-from">{t('commandObs.fromTime')}</Label>
              <Input id="co-from" type="datetime-local" value={from} onChange={(e) => setFrom(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="co-to">{t('commandObs.toTime')}</Label>
              <Input id="co-to" type="datetime-local" value={to} onChange={(e) => setTo(e.target.value)} />
            </div>
            <Button type="submit">{t('common.query')}</Button>
          </form>

          <AsyncSection
            isLoading={historyQuery.isLoading}
            isError={historyQuery.isError}
            error={historyQuery.error}
            skeleton={<TableSkeleton columns={columns.length} />}
          >
            <DataTable
              columns={columns}
              rows={historyQuery.data?.items}
              rowKey={(c) => String(c.commandId)}
              emptyText={t('commandObs.historyEmpty')}
            />
            <div className="mt-4 flex items-center justify-center gap-4 text-sm">
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={page <= 1 || historyQuery.isFetching}
                onClick={() => goPage(page - 1)}
              >
                {t('common.prevPage')}
              </Button>
              <span className="text-muted-foreground">
                {t('common.pageInfo', { page, total: totalPages, count: historyTotal })}
              </span>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={page >= totalPages || historyQuery.isFetching}
                onClick={() => goPage(page + 1)}
              >
                {t('common.nextPage')}
              </Button>
            </div>
          </AsyncSection>
        </CardContent>
      </Card>
    </div>
  )
}

// ageSeconds 客户端按 createdAt 算命令已等时长（秒）：不依赖服务端 ageSeconds（实时队列每刷一次重算更准）。
function ageSeconds(c: CommandMetaView): number {
  const created = new Date(c.createdAt).getTime()
  if (Number.isNaN(created)) return 0
  return Math.max(0, Math.floor((Date.now() - created) / 1000))
}
