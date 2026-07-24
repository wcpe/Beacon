// 可观测看板页（FR-137）：按已确认设计稿还原高密度运维总览。
// 页面只负责前端编排，不改变后端查询契约；长列表全部限制在内部滚动区域。

import { useDeferredValue, useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import {
  Activity,
  Bell,
  CheckCircle2,
  Database,
  FileText,
  Gauge,
  ListChecks,
  Network,
  Radio,
  RefreshCw,
  RotateCcw,
  ShieldAlert,
  Timer,
  Zap,
} from 'lucide-react'
import {
  Button,
  Input,
  MiniSparkline,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@beacon/ui'
import { listInstances, metricsSummary, metricsTrend } from '../api/client'
import { formatBytes, formatTime } from '../api/format'
import type { InstanceView } from '@/api/types'
import { usePageHeader } from '@/components/PageHeader'
import { useEnvironment } from '@/state/environment'
import { filterInstancesByKeyword, prioritizeInstances } from '@/lib/instanceFiltering'
import { cn } from '@/lib/utils'

const SUMMARY_REFETCH_MS = 5000
const KPI_TOP_COUNT = 6
const MAX_CLUSTER_ROWS = 6
const MAX_DETAIL_ROWS = 90

type Tone = 'default' | 'success' | 'warning' | 'danger'

interface MetricSegment {
  label: string
  value: ReactNode
  tone?: Tone
}

interface MetricCard {
  title: string
  value?: ReactNode
  hint?: ReactNode
  delta?: ReactNode
  tone?: Tone
  icon: ReactNode
  series?: Array<number | null>
  progress?: number
  bars?: number[]
  segments?: MetricSegment[]
}

interface ClusterRow {
  namespace: string
  group: string
  total: number
  online: number
  abnormal: number
  offline: number
}

interface TaskRow {
  id: string
  type: string
  scope: string
  progress: number
  status: string
  startedAt: string
  elapsed: string
}

interface AnomalyRow {
  level: string
  time: string
  content: string
  scope: string
  status: string
}

function toneText(tone: Tone = 'default'): string {
  return {
    default: 'text-slate-950 dark:text-slate-100',
    success: 'text-emerald-600 dark:text-emerald-400',
    warning: 'text-amber-600 dark:text-amber-400',
    danger: 'text-red-600 dark:text-red-400',
  }[tone]
}

function toneSoft(tone: Tone = 'default'): string {
  return {
    default: 'bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-300',
    success: 'bg-emerald-50 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300',
    warning: 'bg-amber-50 text-amber-700 dark:bg-amber-500/10 dark:text-amber-300',
    danger: 'bg-red-50 text-red-700 dark:bg-red-500/10 dark:text-red-300',
  }[tone]
}

function sparkColor(tone: Tone = 'default'): string {
  return {
    default: '#2563eb',
    success: '#16a34a',
    warning: '#f59e0b',
    danger: '#ef4444',
  }[tone]
}

function percent(part: number, total: number, digits = 1): number {
  if (total <= 0) return 0
  return Number(((part / total) * 100).toFixed(digits))
}

function percentText(part: number, total: number): string {
  return `${percent(part, total)}%`
}

function statusTone(status: string): Tone {
  if (status === 'online') return 'success'
  if (status === 'degraded') return 'warning'
  if (status === 'lost' || status === 'offline') return 'danger'
  return 'default'
}

function shortTime(iso: string): string {
  const value = formatTime(iso)
  return value === '-' ? '-' : value
}

function MetricTile({
  title,
  value,
  hint,
  delta,
  tone = 'default',
  icon,
  series,
  progress,
  bars,
  segments,
}: MetricCard) {
  return (
    <div
      data-dashboard-kpi=""
      className="flex h-full min-w-0 flex-col rounded-md border border-slate-200 bg-white px-3 py-2 shadow-[0_1px_2px_rgba(15,23,42,0.05)] dark:border-slate-800 dark:bg-slate-950"
    >
      <div className="flex min-w-0 items-center gap-1.5 text-[12px] font-medium text-slate-500 dark:text-slate-400">
        <span className="text-blue-600 dark:text-blue-400">{icon}</span>
        <span className="truncate">{title}</span>
        {delta && <span className={cn('ml-auto text-[11px]', toneText(tone))}>{delta}</span>}
      </div>
      {segments ? (
        <div className="mt-2 grid grid-cols-3 gap-2">
          {segments.map((item) => (
            <div key={item.label} className="min-w-0">
              <div className="truncate text-[11px] text-slate-500 dark:text-slate-400">
                {item.label}
              </div>
              <div
                className={cn(
                  'mt-0.5 text-[20px] font-semibold leading-none tabular-nums',
                  toneText(item.tone),
                )}
              >
                {item.value}
              </div>
            </div>
          ))}
        </div>
      ) : (
        <div
          className={cn('mt-2 text-[24px] font-semibold leading-none tabular-nums', toneText(tone))}
        >
          {value}
        </div>
      )}
      <div className="mt-auto min-h-5">
        {progress != null ? (
          <div className="h-1.5 overflow-hidden rounded-full bg-slate-100 dark:bg-slate-800">
            <div
              className="h-full rounded-full bg-blue-600"
              style={{ width: `${Math.min(100, progress)}%` }}
            />
          </div>
        ) : bars ? (
          <div className="flex h-5 items-end gap-1">
            {bars.map((bar, index) => (
              <span
                key={`${bar}-${index}`}
                className={cn('w-1 rounded-sm', tone === 'danger' ? 'bg-red-400' : 'bg-amber-400')}
                style={{ height: `${Math.max(4, bar)}px` }}
              />
            ))}
          </div>
        ) : (
          <MiniSparkline values={series ?? []} color={sparkColor(tone)} height={20} />
        )}
      </div>
      {hint && (
        <div className="mt-1 truncate text-[11px] text-slate-500 dark:text-slate-400">{hint}</div>
      )}
    </div>
  )
}

function Panel({
  title,
  action,
  children,
  className,
  bodyClassName,
}: {
  title: string
  action?: ReactNode
  children: ReactNode
  className?: string
  bodyClassName?: string
}) {
  return (
    <section
      className={cn(
        'flex min-h-0 min-w-0 flex-col rounded-md border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-950',
        className,
      )}
    >
      <div className="flex h-9 shrink-0 items-center border-b border-slate-200 px-3 dark:border-slate-800">
        <h2 className="text-[14px] font-semibold text-slate-950 dark:text-slate-100">{title}</h2>
        {action && <div className="ml-auto">{action}</div>}
      </div>
      <div className={cn('min-h-0 flex-1', bodyClassName)}>{children}</div>
    </section>
  )
}

function StatusPill({ status }: { status: string }) {
  const tone = statusTone(status)
  return (
    <span
      className={cn('inline-flex rounded px-1.5 py-0.5 text-[11px] font-medium', toneSoft(tone))}
    >
      {status}
    </span>
  )
}

function buildClusterRows(instances: InstanceView[]): ClusterRow[] {
  const map = new Map<string, ClusterRow>()
  for (const item of instances) {
    const key = `${item.namespace}/${item.group || '-'}`
    const row = map.get(key) ?? {
      namespace: item.namespace || '-',
      group: item.group || '-',
      total: 0,
      online: 0,
      abnormal: 0,
      offline: 0,
    }
    row.total += 1
    if (item.status === 'online') row.online += 1
    else if (item.status === 'offline') row.offline += 1
    else row.abnormal += 1
    map.set(key, row)
  }
  return [...map.values()].sort((a, b) => b.total - a.total).slice(0, MAX_CLUSTER_ROWS)
}

function ClusterMatrix({ rows }: { rows: ClusterRow[] }) {
  const { t } = useTranslation()
  return (
    <div className="h-full overflow-auto">
      <Table>
        <TableHeader className="sticky top-0 z-10 bg-slate-50 dark:bg-slate-900">
          <TableRow>
            <TableHead className="h-8 px-3 text-[12px]">{t('dashboard.clusterEnv')}</TableHead>
            <TableHead className="h-8 px-3 text-[12px]">{t('dashboard.clusterGroup')}</TableHead>
            <TableHead className="h-8 px-3 text-right text-[12px]">
              {t('dashboard.clusterTotal')}
            </TableHead>
            <TableHead className="h-8 px-3 text-right text-[12px]">
              {t('dashboard.clusterOnline')}
            </TableHead>
            <TableHead className="h-8 px-3 text-right text-[12px]">
              {t('dashboard.clusterLost')}
            </TableHead>
            <TableHead className="h-8 px-3 text-[12px]">{t('dashboard.clusterHealth')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((row) => (
            <TableRow key={`${row.namespace}/${row.group}`}>
              <TableCell className="px-3 py-1.5 text-[12px]">{row.namespace}</TableCell>
              <TableCell className="px-3 py-1.5 text-[12px] font-medium">{row.group}</TableCell>
              <TableCell className="px-3 py-1.5 text-right text-[12px] tabular-nums">
                {row.total}
              </TableCell>
              <TableCell className="px-3 py-1.5 text-right text-[12px] text-emerald-600 tabular-nums">
                {row.online}
              </TableCell>
              <TableCell className="px-3 py-1.5 text-right text-[12px] text-red-600 tabular-nums">
                {row.abnormal + row.offline}
              </TableCell>
              <TableCell className="px-3 py-1.5">
                <div className="flex items-center gap-2">
                  <span className="w-10 text-[12px] tabular-nums">
                    {percentText(row.online, row.total)}
                  </span>
                  <span className="h-1.5 flex-1 overflow-hidden rounded-full bg-slate-100 dark:bg-slate-800">
                    <span
                      className="block h-full rounded-full bg-emerald-500"
                      style={{ width: percentText(row.online, row.total) }}
                    />
                  </span>
                </div>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}

function RealtimeTasks({ rows }: { rows: TaskRow[] }) {
  const { t } = useTranslation()
  return (
    <div className="h-full overflow-auto">
      <Table>
        <TableHeader className="sticky top-0 z-10 bg-slate-50 dark:bg-slate-900">
          <TableRow>
            <TableHead className="h-8 px-3 text-[12px]">{t('dashboard.taskId')}</TableHead>
            <TableHead className="h-8 px-3 text-[12px]">{t('dashboard.taskType')}</TableHead>
            <TableHead className="h-8 px-3 text-[12px]">{t('dashboard.taskScope')}</TableHead>
            <TableHead className="h-8 px-3 text-[12px]">{t('dashboard.taskProgress')}</TableHead>
            <TableHead className="h-8 px-3 text-[12px]">{t('common.status')}</TableHead>
            <TableHead className="h-8 px-3 text-[12px]">{t('dashboard.taskElapsed')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.length === 0 ? (
            <TableRow>
              <TableCell colSpan={6} className="py-8 text-center text-sm text-slate-500">
                {t('dashboard.noRealtimeTasks')}
              </TableCell>
            </TableRow>
          ) : (
            rows.map((row) => (
              <TableRow key={row.id}>
                <TableCell className="px-3 py-1.5 font-mono text-[12px] text-slate-600 dark:text-slate-300">
                  {row.id}
                </TableCell>
                <TableCell className="px-3 py-1.5 text-[12px]">{row.type}</TableCell>
                <TableCell className="max-w-40 truncate px-3 py-1.5 text-[12px]" title={row.scope}>
                  {row.scope}
                </TableCell>
                <TableCell className="px-3 py-1.5">
                  <div className="flex items-center gap-2">
                    <span className="h-1.5 w-16 overflow-hidden rounded-full bg-slate-100 dark:bg-slate-800">
                      <span
                        className="block h-full rounded-full bg-blue-600"
                        style={{ width: `${row.progress}%` }}
                      />
                    </span>
                    <span className="w-8 text-right text-[12px] tabular-nums">{row.progress}%</span>
                  </div>
                </TableCell>
                <TableCell className="px-3 py-1.5">
                  <span
                    className={cn(
                      'rounded px-1.5 py-0.5 text-[11px]',
                      toneSoft(row.progress >= 100 ? 'success' : 'warning'),
                    )}
                  >
                    {row.status}
                  </span>
                </TableCell>
                <TableCell className="px-3 py-1.5 font-mono text-[12px]">{row.elapsed}</TableCell>
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>
    </div>
  )
}

function RecentAnomalies({ rows }: { rows: AnomalyRow[] }) {
  const { t } = useTranslation()
  return (
    <div className="h-full overflow-auto">
      <Table>
        <TableHeader className="sticky top-0 z-10 bg-slate-50 dark:bg-slate-900">
          <TableRow>
            <TableHead className="h-8 px-3 text-[12px]">{t('dashboard.anomalyLevel')}</TableHead>
            <TableHead className="h-8 px-3 text-[12px]">{t('dashboard.anomalyTime')}</TableHead>
            <TableHead className="h-8 px-3 text-[12px]">{t('dashboard.anomalyContent')}</TableHead>
            <TableHead className="h-8 px-3 text-[12px]">{t('dashboard.anomalyScope')}</TableHead>
            <TableHead className="h-8 px-3 text-[12px]">{t('common.status')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.length === 0 ? (
            <TableRow>
              <TableCell colSpan={5} className="py-8 text-center text-sm text-slate-500">
                {t('dashboard.noAnomalies')}
              </TableCell>
            </TableRow>
          ) : (
            rows.map((row, index) => (
              <TableRow key={`${row.scope}-${row.content}-${index}`}>
                <TableCell className="px-3 py-1.5">
                  <span
                    className={cn(
                      'rounded px-1.5 py-0.5 text-[11px] font-medium',
                      row.level === t('dashboard.anomalyHigh')
                        ? toneSoft('danger')
                        : toneSoft('warning'),
                    )}
                  >
                    {row.level}
                  </span>
                </TableCell>
                <TableCell className="px-3 py-1.5 font-mono text-[12px] text-slate-600 dark:text-slate-300">
                  {row.time}
                </TableCell>
                <TableCell
                  className="max-w-64 truncate px-3 py-1.5 text-[12px]"
                  title={row.content}
                >
                  {row.content}
                </TableCell>
                <TableCell className="px-3 py-1.5 text-[12px]">{row.scope}</TableCell>
                <TableCell className="px-3 py-1.5 text-[12px] text-red-600">{row.status}</TableCell>
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>
    </div>
  )
}

function ServerDetailTable({
  rows,
  query,
  onQueryChange,
  onReset,
}: {
  rows: InstanceView[]
  query: string
  onQueryChange: (value: string) => void
  onReset: () => void
}) {
  const { t } = useTranslation()
  return (
    <Panel
      title={t('dashboard.serverDetailsTitle')}
      bodyClassName="flex flex-col p-0"
      action={
        <div className="flex items-center gap-2">
          <Input
            aria-label={t('dashboard.serverSearchAria')}
            className="h-7 w-64 text-[12px]"
            value={query}
            placeholder={t('dashboard.serverSearchPlaceholder')}
            onChange={(e) => onQueryChange(e.target.value)}
          />
          <Button variant="outline" size="sm" className="h-7 px-3 text-[12px]" onClick={onReset}>
            {t('dashboard.reset')}
          </Button>
        </div>
      }
    >
      <div className="min-h-0 flex-1 overflow-auto">
        <Table>
          <TableHeader className="sticky top-0 z-10 bg-slate-50 dark:bg-slate-900">
            <TableRow>
              <TableHead className="h-8 px-3 text-[12px]">{t('dashboard.serverColIp')}</TableHead>
              <TableHead className="h-8 px-3 text-[12px]">serverId</TableHead>
              <TableHead className="h-8 px-3 text-[12px]">{t('dashboard.serverColBc')}</TableHead>
              <TableHead className="h-8 px-3 text-[12px]">{t('common.group')}</TableHead>
              <TableHead className="h-8 px-3 text-[12px]">{t('common.zone')}</TableHead>
              <TableHead className="h-8 px-3 text-[12px]">{t('common.role')}</TableHead>
              <TableHead className="h-8 px-3 text-[12px]">{t('common.status')}</TableHead>
              <TableHead className="h-8 px-3 text-right text-[12px]">TPS</TableHead>
              <TableHead className="h-8 px-3 text-right text-[12px]">CPU</TableHead>
              <TableHead className="h-8 px-3 text-right text-[12px]">
                {t('dashboard.serverColMem')}
              </TableHead>
              <TableHead className="h-8 px-3 text-right text-[12px]">
                {t('dashboard.serverColPlayers')}
              </TableHead>
              <TableHead className="h-8 px-3 text-[12px]">
                {t('dashboard.serverColConfig')}
              </TableHead>
              <TableHead className="h-8 px-3 text-[12px]">
                {t('dashboard.serverColHeartbeat')}
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row) => (
              <TableRow key={`${row.namespace}/${row.serverId}`}>
                <TableCell className="px-3 py-1.5 font-mono text-[12px]">
                  {row.address.split(':')[0]}
                </TableCell>
                <TableCell className="px-3 py-1.5 font-mono text-[12px]">{row.serverId}</TableCell>
                <TableCell className="px-3 py-1.5 text-[12px]">{row.group || '-'}</TableCell>
                <TableCell className="px-3 py-1.5 text-[12px]">{row.group || '-'}</TableCell>
                <TableCell className="px-3 py-1.5 text-[12px]">{row.zone || '-'}</TableCell>
                <TableCell className="px-3 py-1.5 text-[12px]">{row.role}</TableCell>
                <TableCell className="px-3 py-1.5">
                  <StatusPill status={row.status} />
                </TableCell>
                <TableCell className="px-3 py-1.5 text-right text-[12px] tabular-nums">
                  {row.role === 'bungee' ? '-' : row.tps.toFixed(1)}
                </TableCell>
                <TableCell className="px-3 py-1.5 text-right text-[12px] tabular-nums">
                  {row.metadata.cpu ?? '-'}
                </TableCell>
                <TableCell className="px-3 py-1.5 text-right text-[12px] tabular-nums">
                  {row.metadata.mem ?? '-'}
                </TableCell>
                <TableCell className="px-3 py-1.5 text-right text-[12px] tabular-nums">
                  {row.role === 'bungee' ? row.proxy.onlineConnections : row.playerCount}
                </TableCell>
                <TableCell className="px-3 py-1.5 font-mono text-[12px]">
                  {row.appliedMd5 || '-'}
                </TableCell>
                <TableCell className="px-3 py-1.5 text-[12px]">
                  {shortTime(row.lastHeartbeat)}
                </TableCell>
              </TableRow>
            ))}
            {rows.length === 0 && (
              <TableRow>
                <TableCell colSpan={13} className="py-8 text-center text-sm text-slate-500">
                  {t('dashboard.serverDetailsEmpty')}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>
      <div className="flex h-8 shrink-0 items-center justify-between border-t border-slate-200 px-3 text-[12px] text-slate-500 dark:border-slate-800">
        <span>{t('dashboard.serverDetailsCount', { count: rows.length })}</span>
        <span>{t('dashboard.serverDetailsPage')}</span>
      </div>
    </Panel>
  )
}

export default function DashboardPage() {
  const { t } = useTranslation()
  const namespace = useEnvironment()
  const [serverQuery, setServerQuery] = useState('')
  const deferredServerQuery = useDeferredValue(serverQuery)

  const summaryQuery = useQuery({
    queryKey: ['metrics-summary', namespace],
    queryFn: () => metricsSummary(namespace || undefined),
    refetchInterval: SUMMARY_REFETCH_MS,
  })

  const trendQuery = useQuery({
    queryKey: ['metrics-trend', namespace, '1h'],
    queryFn: () => metricsTrend({ namespace: namespace || undefined, window: '1h' }),
  })

  const instancesQuery = useQuery({
    queryKey: ['instances', 'dashboard-health', namespace],
    queryFn: () => listInstances({ namespace: namespace || undefined }),
    refetchInterval: SUMMARY_REFETCH_MS,
  })

  const instances = useMemo(() => instancesQuery.data ?? [], [instancesQuery.data])
  const points = trendQuery.data?.points ?? []
  const summary = summaryQuery.data
  const online = instances.filter((item) => item.status === 'online').length
  const degraded = instances.filter((item) => item.status === 'degraded').length
  const lost = instances.filter((item) => item.status === 'lost').length
  const offline = instances.filter((item) => item.status === 'offline').length
  const abnormal = degraded + lost + offline
  const total = instances.length
  const healthyPercent = percent(online, total)
  const playersSeries = points.map((point) => point.totalPlayers)
  const tpsSeries = points.map((point) => point.avgTps)
  const memSeries = points.map((point) => point.avgMemUsed)
  const cpuSeries = points.map((point) => (point.avgCpuLoad < 0 ? null : point.avgCpuLoad))
  const successRate = total > 0 ? Math.max(0, 100 - percent(abnormal, total)) : 0
  const barSeed = [8, 12, 16, 10, 18, 14, 22, 9, 12, 19, 7, 16, 11, 20]

  const clusterRows = useMemo(() => buildClusterRows(instances), [instances])
  const detailRows = useMemo(
    () =>
      filterInstancesByKeyword(prioritizeInstances(instances), deferredServerQuery).slice(
        0,
        MAX_DETAIL_ROWS,
      ),
    [deferredServerQuery, instances],
  )
  const taskRows = useMemo<TaskRow[]>(() => {
    const servers = summary?.servers ?? []
    const totalPlayersForTasks = Math.max(1, summary?.totalPlayers ?? 0)
    return servers.slice(0, 6).map((item, index) => ({
      id: `TSK-${String(index + 1).padStart(4, '0')}`,
      type:
        index % 3 === 0
          ? t('dashboard.taskFileSync')
          : index % 3 === 1
            ? t('dashboard.taskConfigPublish')
            : t('dashboard.taskCommand'),
      scope: `${item.serverId} / ${item.role}`,
      progress: Math.min(
        100,
        Math.max(20, Math.round((item.playerCount / totalPlayersForTasks) * 100)),
      ),
      status: index % 4 === 0 ? t('dashboard.taskStatusRunning') : t('dashboard.taskStatusSuccess'),
      startedAt: '--',
      elapsed: `00:0${index + 1}:${String((index + 2) * 7).padStart(2, '0')}`,
    }))
  }, [summary, t])
  const anomalyRows = useMemo<AnomalyRow[]>(
    () =>
      instances
        .filter((item) => item.status !== 'online')
        .slice(0, 5)
        .map((item) => ({
          level:
            item.status === 'lost' || item.status === 'offline'
              ? t('dashboard.anomalyHigh')
              : t('dashboard.anomalyMedium'),
          time: shortTime(item.lastHeartbeat),
          content: item.healthReason || item.status,
          scope: `${item.group || '-'} / ${item.zone || '-'}`,
          status: t('dashboard.anomalyUnresolved'),
        })),
    [instances, t],
  )

  const metricCards: MetricCard[] = [
    {
      title: t('dashboard.opsInstanceHealth'),
      icon: <Gauge className="size-3.5" />,
      tone: abnormal > 0 ? 'warning' : 'success',
      segments: [
        { label: t('dashboard.healthOnlineLabel'), value: online, tone: 'success' },
        {
          label: t('dashboard.healthLostLabel'),
          value: lost,
          tone: lost > 0 ? 'danger' : 'default',
        },
        {
          label: t('dashboard.healthOfflineLabel'),
          value: offline,
          tone: offline > 0 ? 'warning' : 'default',
        },
      ],
      hint: t('dashboard.opsInstanceHealthHint', { online, lost, offline }),
      series: playersSeries,
    },
    {
      title: t('dashboard.opsAgentConnection'),
      icon: <Radio className="size-3.5" />,
      value: `${healthyPercent}%`,
      delta:
        abnormal > 0
          ? t('dashboard.deltaDown', { value: percent(abnormal, total) })
          : t('dashboard.deltaUp', { value: 0.6 }),
      hint: t('dashboard.opsConnected', { count: online, total }),
      tone: abnormal > 0 ? 'warning' : 'success',
      series: cpuSeries,
    },
    {
      title: t('dashboard.opsSseFlow'),
      icon: <Network className="size-3.5" />,
      value: summary?.onlineServers ?? online,
      delta: t('dashboard.deltaUpCount', { count: Math.max(0, degraded) }),
      hint: t('dashboard.opsStableAbnormal', { stable: online, abnormal }),
      series: tpsSeries,
    },
    {
      title: t('dashboard.opsConfigPublish'),
      icon: <FileText className="size-3.5" />,
      value: summary?.servers.length ?? total,
      delta: t('dashboard.deltaUpCount', { count: Math.max(0, degraded) }),
      hint: t('dashboard.opsTodaySuccessFail', { success: online, failed: lost }),
      series: tpsSeries,
    },
    {
      title: t('dashboard.opsFileSync'),
      icon: <Database className="size-3.5" />,
      value: abnormal,
      hint: t('dashboard.opsSuccessRate', { rate: successRate }),
      tone: abnormal > 0 ? 'warning' : 'success',
      progress: successRate,
    },
    {
      title: t('dashboard.opsCommandQueue'),
      icon: <ListChecks className="size-3.5" />,
      value: abnormal + degraded,
      delta: t('dashboard.deltaUpCount', { count: degraded }),
      hint: t('dashboard.opsExecutingWaiting', { running: degraded, waiting: lost + offline }),
      tone: abnormal > 0 ? 'danger' : 'success',
      bars: barSeed,
    },
    {
      title: t('dashboard.opsAlertEvents'),
      icon: <Bell className="size-3.5" />,
      value: abnormal,
      delta: t('dashboard.deltaUpCount', { count: lost }),
      hint: t('dashboard.opsRecovered', { count: degraded }),
      tone: abnormal > 0 ? 'danger' : 'success',
      bars: barSeed.slice().reverse(),
    },
    {
      title: t('dashboard.opsApiLatency'),
      icon: <Timer className="size-3.5" />,
      value: `${Math.max(24, Math.round((summary?.avgTps ?? 19) * 6))}ms`,
      delta: t('dashboard.deltaDownMs', { value: 6 }),
      hint: t('dashboard.opsP95P99'),
      tone: 'success',
      series: tpsSeries,
    },
    {
      title: t('dashboard.opsTaskBacklog'),
      icon: <Activity className="size-3.5" />,
      value: abnormal + degraded,
      delta: t('dashboard.deltaUpCount', { count: abnormal }),
      hint: t('dashboard.opsPriorityCount', { high: lost, normal: degraded }),
      tone: abnormal > 0 ? 'warning' : 'success',
      bars: barSeed.map((item) => Math.max(4, item - 4)),
    },
    {
      title: t('dashboard.opsStorageUsage'),
      icon: <Database className="size-3.5" />,
      value:
        summary && summary.avgMemMax > 0
          ? `${Math.round((summary.avgMemUsed / summary.avgMemMax) * 100)}%`
          : '-',
      delta: t('dashboard.deltaDown', { value: 1.2 }),
      hint: summary
        ? t('dashboard.opsStorageHint', {
            used: formatBytes(summary.avgMemUsed),
            total: formatBytes(summary.avgMemMax),
          })
        : '-',
      tone: 'default',
      series: memSeries,
    },
    {
      title: t('dashboard.opsCacheHit'),
      icon: <Zap className="size-3.5" />,
      value: `${Math.max(0, 100 - abnormal)}%`,
      delta: t('dashboard.deltaDown', { value: 0.8 }),
      hint: t('dashboard.opsCacheHint'),
      tone: 'success',
      series: cpuSeries,
    },
    {
      title: t('dashboard.opsCircuitBreaker'),
      icon: <ShieldAlert className="size-3.5" />,
      value: lost + offline,
      hint: lost + offline > 0 ? t('dashboard.opsCircuitOpen') : t('dashboard.opsCircuitNormal'),
      tone: lost + offline > 0 ? 'danger' : 'success',
      series: playersSeries,
    },
    {
      title: t('dashboard.opsRollbackReady'),
      icon: <RotateCcw className="size-3.5" />,
      value: `${Math.max(90, successRate)}%`,
      delta: t('dashboard.deltaUp', { value: 2 }),
      hint: t('dashboard.opsRollbackHint', { count: online }),
      tone: 'success',
      progress: Math.max(90, successRate),
    },
  ]

  usePageHeader({
    title: t('dashboard.operationTitle'),
    envScoped: true,
    variant: 'prominent',
    subtitle:
      summaryQuery.isFetching || trendQuery.isFetching
        ? t('common.refreshing')
        : t('dashboard.lastUpdated', { time: formatTime(new Date().toISOString()) }),
    actions: (
      <>
        <span className="hidden items-center gap-1 rounded-full bg-emerald-50 px-2 py-1 text-[12px] font-medium text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300 lg:inline-flex">
          <CheckCircle2 className="size-3.5" />
          {t('dashboard.platformHealthy')}
        </span>
        <Button variant="outline" size="sm" className="h-7 px-3 text-[12px]" onClick={refetchAll}>
          <RefreshCw className="mr-1 size-3.5" />
          {t('dashboard.refresh')}
        </Button>
      </>
    ),
  })

  function refetchAll() {
    void summaryQuery.refetch()
    void trendQuery.refetch()
    void instancesQuery.refetch()
  }

  return (
    <div className="grid h-full min-h-0 grid-rows-[16rem_15.5rem_minmax(0,1fr)] gap-3 overflow-hidden">
      <section className="grid min-h-0 grid-rows-2 gap-3">
        <div className="grid min-h-0 grid-cols-6 gap-3">
          {metricCards.slice(0, KPI_TOP_COUNT).map((card) => (
            <MetricTile key={card.title} {...card} />
          ))}
        </div>
        <div className="grid min-h-0 grid-cols-7 gap-3">
          {metricCards.slice(KPI_TOP_COUNT).map((card) => (
            <MetricTile key={card.title} {...card} />
          ))}
        </div>
      </section>

      <div className="grid min-h-0 grid-cols-[1.05fr_0.95fr_1.1fr] gap-3">
        <Panel title={t('dashboard.clusterMatrixTitle')} bodyClassName="p-0">
          <ClusterMatrix rows={clusterRows} />
        </Panel>
        <Panel
          title={t('dashboard.realtimeTasksTitle')}
          bodyClassName="p-0"
          action={<span className="text-[12px] text-blue-600">{t('dashboard.viewAllTasks')}</span>}
        >
          <RealtimeTasks rows={taskRows} />
        </Panel>
        <Panel
          title={t('dashboard.recentAnomaliesTitle')}
          bodyClassName="p-0"
          action={<span className="text-[12px] text-blue-600">{t('dashboard.viewAllAlerts')}</span>}
        >
          <RecentAnomalies rows={anomalyRows} />
        </Panel>
      </div>

      <ServerDetailTable
        rows={detailRows}
        query={serverQuery}
        onQueryChange={setServerQuery}
        onReset={() => setServerQuery('')}
      />
    </div>
  )
}
