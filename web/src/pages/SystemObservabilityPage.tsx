// 控制面健康页（FR-82）：控制面进程**自身**内部运行态的只读自观测（ADR-0048 恢复为独立页 /system）。
// 顶部加「仪表环总览行」（DB 连接池 / 长轮询挂起 / 命令队列 / 注册表）：一眼看哪个子系统吃紧（按阈值变色）。
// 下面保留四组指标详细明细卡 + 进程运行时卡——每项前加 lucide 图标、数值按阈值上健康色
// （等待次数>0 标注意、失联实例>0 标危险等），让运维「一眼看清当前情况」。
// 与 FR-32 可观测看板（agent 网络负载）、FR-73 服务分析（平台运维活动）清晰区分：本页只看 Beacon 自己卡不卡，只读、不参与决策。

import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import {
  Activity,
  AlarmClock,
  CircleCheck,
  CircleX,
  Clock,
  Cpu,
  Database,
  HardDrive,
  Hourglass,
  Layers,
  ListChecks,
  ListTodo,
  Plug,
  Radio,
  Send,
  Tag,
  TriangleAlert,
} from 'lucide-react'
import { systemObservability, systemStatus } from '@/api/client'
import { formatBytes, formatDuration } from '@/api/format'
import AsyncSection from '@/components/AsyncSection'
import GaugeRing from '@/components/dashboard/GaugeRing'
import { countLevel, levelText, ratioLevel, statusLevel, type HealthLevel } from '@/components/dashboard/health'
import { cn } from '@/lib/utils'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'

// 自观测快照刷新周期（毫秒）：本页打开时短周期轮询，反映控制面当前内部态（不进 FR-33 页眉高频轮询）。
const OBS_REFETCH_MS = 5000

// 健康状态展示顺序（与注册表健康状态机一致：online→degraded→lost→offline）。
const STATUS_ORDER = ['online', 'degraded', 'lost', 'offline'] as const
// 命令队列状态展示顺序（与 agent_command 状态机一致）。
const COMMAND_ORDER = ['pending', 'fetched', 'ready', 'done', 'failed', 'expired'] as const

// 注册表状态 → 行图标（与状态语义对应）。
const STATUS_ICON: Record<(typeof STATUS_ORDER)[number], React.ReactNode> = {
  online: <CircleCheck className="size-4" />,
  degraded: <TriangleAlert className="size-4" />,
  lost: <CircleX className="size-4" />,
  offline: <CircleX className="size-4" />,
}
// 命令队列状态 → 行图标。
const COMMAND_ICON: Record<(typeof COMMAND_ORDER)[number], React.ReactNode> = {
  pending: <ListTodo className="size-4" />,
  fetched: <Activity className="size-4" />,
  ready: <Clock className="size-4" />,
  done: <CircleCheck className="size-4" />,
  failed: <CircleX className="size-4" />,
  expired: <AlarmClock className="size-4" />,
}

// 详细明细一行：图标 + 标签 + 值 + 说明（说明可选）。值用等宽数字便于纵向对齐；可按等级给值上色。
function MetricRow({
  icon,
  label,
  value,
  hint,
  level,
}: {
  icon?: React.ReactNode
  label: string
  value: React.ReactNode
  hint?: string
  level?: HealthLevel
}) {
  return (
    <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 py-2">
      <div className="flex min-w-0 items-baseline gap-2">
        {icon && (
          <span aria-hidden className="self-center text-muted-foreground">
            {icon}
          </span>
        )}
        <div className="min-w-0">
          <div className="text-sm">{label}</div>
          {hint && <div className="mt-0.5 text-xs text-muted-foreground">{hint}</div>}
        </div>
      </div>
      <div className={cn('text-sm font-medium tabular-nums', level && levelText(level))}>{value}</div>
    </div>
  )
}

export default function SystemObservabilityPage() {
  const { t } = useTranslation()
  const { data, isLoading, isError, error, isFetching } = useQuery({
    queryKey: ['system-observability'],
    queryFn: systemObservability,
    refetchInterval: OBS_REFETCH_MS,
  })

  // 进程运行时卡数据：复用 FR-33 页眉同一 ['system-status'] 缓存（页眉精简后这些指标迁来此处）。
  const { data: status } = useQuery({
    queryKey: ['system-status'],
    queryFn: systemStatus,
    refetchInterval: OBS_REFETCH_MS,
  })

  // ===== 仪表环总览行派生：占比 / 等级 =====
  // DB 连接池占用率：已建 / 上限（上限 0 表无限，占比不可算 → null）。
  const dbRatio = data && data.dbPool.maxOpenConnections > 0 ? data.dbPool.openConnections / data.dbPool.maxOpenConnections : null
  // 等待次数 > 0 即标注意（连接池有过排队）。
  const dbLevel: HealthLevel = data ? (data.dbPool.waitCount > 0 ? 'warn' : ratioLevel(dbRatio ?? 0)) : 'muted'
  // 长轮询挂起：无固定上限，仅作计数环（满灰底，等级恒正常——挂起是常态）。
  const longpollLevel: HealthLevel = 'ok'
  // 命令队列：失败 / 过期 > 0 标危险，待拉取积压标注意。
  const cmdFailed = (data?.commandByStatus.failed ?? 0) + (data?.commandByStatus.expired ?? 0)
  const cmdPending = data?.commandByStatus.pending ?? 0
  const cmdLevel: HealthLevel = data ? (cmdFailed > 0 ? 'danger' : countLevel(cmdPending)) : 'muted'
  // 注册表：失联 + 离线 > 0 标危险，亚健康 > 0 标注意。
  const regLost = (data?.registryByStatus.lost ?? 0) + (data?.registryByStatus.offline ?? 0)
  const regDegraded = data?.registryByStatus.degraded ?? 0
  const regLevel: HealthLevel = data ? (regLost > 0 ? 'danger' : regDegraded > 0 ? 'warn' : 'ok') : 'muted'

  return (
    <div className="space-y-4">
      <div className="space-y-1">
        <h1 className="text-xl font-semibold">{t('observability.title')}</h1>
        <div className="flex items-center gap-3">
          <p className="text-sm text-muted-foreground">{t('observability.subtitle')}</p>
          {isFetching && <span className="text-sm text-muted-foreground">{t('common.refreshing')}</span>}
        </div>
      </div>

      <AsyncSection isLoading={isLoading} isError={isError} error={error}>
        {data && (
          <div className="space-y-4">
            {/* ===== 仪表环总览行：四子系统吃紧一眼看（按阈值变色） ===== */}
            <Card>
              <CardHeader>
                <CardTitle className="text-base">{t('observability.gaugeRowTitle')}</CardTitle>
              </CardHeader>
              <CardContent>
                <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
                  <GaugeRing
                    icon={<Database className="size-6" />}
                    ratio={dbRatio}
                    level={dbLevel}
                    label={t('observability.gaugeDbPool')}
                    valueText={`${data.dbPool.openConnections} / ${data.dbPool.maxOpenConnections > 0 ? data.dbPool.maxOpenConnections : '∞'}`}
                    hint={dbRatio === null ? t('observability.gaugeNoLimit') : t('dashboard.bcReachPercent', { percent: Math.round(dbRatio * 100) })}
                  />
                  <GaugeRing
                    icon={<Hourglass className="size-6" />}
                    ratio={null}
                    level={longpollLevel}
                    label={t('observability.gaugeLongpoll')}
                    valueText={String(data.longpoll.total)}
                  />
                  <GaugeRing
                    icon={<ListChecks className="size-6" />}
                    ratio={null}
                    level={cmdLevel}
                    label={t('observability.gaugeCommand')}
                    valueText={String(
                      COMMAND_ORDER.reduce((sum, s) => sum + (data.commandByStatus[s] ?? 0), 0),
                    )}
                    hint={cmdFailed > 0 ? t('observability.gaugeCmdFailed', { count: cmdFailed }) : undefined}
                  />
                  <GaugeRing
                    icon={<Layers className="size-6" />}
                    ratio={null}
                    level={regLevel}
                    label={t('observability.gaugeRegistry')}
                    valueText={String(data.registryTotal)}
                    hint={regLost > 0 ? t('observability.gaugeRegLost', { count: regLost }) : undefined}
                  />
                </div>
              </CardContent>
            </Card>

            {/* ===== 详细明细卡（带图标 + 阈值上色） ===== */}
            <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
              {/* 进程运行时：版本 / 运行时长 / 采样器 / Go 运行时资源 / 进程 CPU（由 FR-33 页眉精简迁入） */}
              <Card>
                <CardHeader>
                  <CardTitle className="text-base">{t('observability.runtimeTitle')}</CardTitle>
                </CardHeader>
                <CardContent className="divide-y py-0">
                  <MetricRow
                    icon={<Tag className="size-4" />}
                    label={t('observability.runtimeVersion')}
                    value={status?.version ?? '-'}
                    hint={t('observability.runtimeVersionHint')}
                  />
                  <MetricRow
                    icon={<Clock className="size-4" />}
                    label={t('observability.runtimeUptime')}
                    value={formatDuration(status?.uptimeSeconds)}
                    hint={t('observability.runtimeUptimeHint')}
                  />
                  <MetricRow
                    icon={<Radio className="size-4" />}
                    label={t('observability.runtimeSampler')}
                    value={
                      status
                        ? status.samplerEnabled
                          ? t('systemHeader.samplerEnabled')
                          : t('systemHeader.samplerDisabled')
                        : '-'
                    }
                    hint={t('observability.runtimeSamplerHint')}
                    level={status ? (status.samplerEnabled ? 'ok' : 'muted') : undefined}
                  />
                  <MetricRow
                    icon={<Activity className="size-4" />}
                    label={t('observability.runtimeGoroutine')}
                    value={status?.runtime.goroutines ?? '-'}
                    hint={t('observability.runtimeGoroutineHint')}
                  />
                  <MetricRow
                    icon={<HardDrive className="size-4" />}
                    label={t('observability.runtimeHeap')}
                    value={
                      status ? `${formatBytes(status.runtime.heapAlloc)} / ${formatBytes(status.runtime.heapSys)}` : '-'
                    }
                    hint={t('observability.runtimeHeapHint')}
                  />
                  <MetricRow
                    icon={<Cpu className="size-4" />}
                    label={t('observability.runtimeCpu')}
                    // cpuAvailable=false 表示采集失败，降级为「不可用」（gopsutil 容器内常见）
                    value={
                      status ? (status.cpuAvailable ? `${status.cpuPercent.toFixed(1)}%` : t('systemHeader.unavailable')) : '-'
                    }
                    hint={t('observability.runtimeCpuHint')}
                  />
                </CardContent>
              </Card>

              {/* 数据库连接池：逐项明细（等待次数 > 0 标注意） */}
              <Card>
                <CardHeader>
                  <CardTitle className="text-base">{t('observability.dbPoolTitle')}</CardTitle>
                </CardHeader>
                <CardContent className="divide-y py-0">
                  <MetricRow
                    icon={<Plug className="size-4" />}
                    label={t('observability.dbOpen')}
                    value={data.dbPool.openConnections}
                    hint={t('observability.dbOpenHint')}
                  />
                  <MetricRow
                    icon={<Database className="size-4" />}
                    label={t('observability.dbMaxLabel')}
                    // maxOpenConnections=0 表示无限（database/sql 约定）
                    value={data.dbPool.maxOpenConnections > 0 ? data.dbPool.maxOpenConnections : '∞'}
                    hint={t('observability.dbMaxHint')}
                  />
                  <MetricRow
                    icon={<Activity className="size-4" />}
                    label={t('observability.dbInUse')}
                    value={data.dbPool.inUse}
                    hint={t('observability.dbInUseHint')}
                  />
                  <MetricRow
                    icon={<CircleCheck className="size-4" />}
                    label={t('observability.dbIdle')}
                    value={data.dbPool.idle}
                    hint={t('observability.dbIdleHint')}
                  />
                  <MetricRow
                    icon={<Hourglass className="size-4" />}
                    label={t('observability.dbWait')}
                    value={data.dbPool.waitCount}
                    hint={t('observability.dbWaitCountHint')}
                    // 累计等待次数 > 0 即连接池有过排队，标注意
                    level={countLevel(data.dbPool.waitCount)}
                  />
                  <MetricRow
                    icon={<Clock className="size-4" />}
                    label={t('observability.dbWaitDuration')}
                    value={`${data.dbPool.waitDurationMs} ms`}
                    hint={t('observability.dbWaitDurationHint')}
                    level={countLevel(data.dbPool.waitDurationMs)}
                  />
                </CardContent>
              </Card>

              {/* 长轮询挂起：四通道逐项明细 */}
              <Card>
                <CardHeader>
                  <CardTitle className="text-base">{t('observability.longpollTitle')}</CardTitle>
                </CardHeader>
                <CardContent className="divide-y py-0">
                  <MetricRow
                    icon={<Hourglass className="size-4" />}
                    label={t('observability.longpollTotal')}
                    value={data.longpoll.total}
                    hint={t('observability.longpollTotalHint')}
                  />
                  <MetricRow icon={<Tag className="size-4" />} label={t('observability.longpollConfig')} value={data.longpoll.config} />
                  <MetricRow icon={<HardDrive className="size-4" />} label={t('observability.longpollFile')} value={data.longpoll.file} />
                  <MetricRow icon={<Layers className="size-4" />} label={t('observability.longpollTopology')} value={data.longpoll.topology} />
                  <MetricRow icon={<Send className="size-4" />} label={t('observability.longpollCommand')} value={data.longpoll.command} />
                </CardContent>
              </Card>

              {/* 注册表规模：总数 + 按健康状态逐项（失联 / 离线 > 0 标危险） */}
              <Card>
                <CardHeader>
                  <CardTitle className="text-base">{t('observability.registryTitle')}</CardTitle>
                </CardHeader>
                <CardContent className="divide-y py-0">
                  <MetricRow
                    icon={<Layers className="size-4" />}
                    label={t('observability.registryTotal')}
                    value={data.registryTotal}
                    hint={t('observability.registryTotalHint')}
                  />
                  {STATUS_ORDER.map((s) => {
                    const count = data.registryByStatus[s] ?? 0
                    // 失联 / 离线有计数标危险，亚健康有计数标注意，在线正常；计数为 0 不上色（中性）。
                    const level: HealthLevel | undefined = count > 0 ? statusLevel(s) : undefined
                    return (
                      <MetricRow
                        key={s}
                        icon={STATUS_ICON[s]}
                        label={t(`observability.status.${s}`)}
                        value={count}
                        level={level}
                      />
                    )
                  })}
                </CardContent>
              </Card>

              {/* 命令队列深度：按状态逐项明细（失败 / 过期 > 0 标危险） */}
              <Card>
                <CardHeader>
                  <CardTitle className="text-base">{t('observability.commandTitle')}</CardTitle>
                </CardHeader>
                <CardContent className="divide-y py-0">
                  {COMMAND_ORDER.map((s) => {
                    const count = data.commandByStatus[s] ?? 0
                    // 失败 / 过期有计数标危险；其余正常（不刻意上色）。
                    const level: HealthLevel | undefined =
                      (s === 'failed' || s === 'expired') && count > 0 ? 'danger' : undefined
                    return (
                      <MetricRow
                        key={s}
                        icon={COMMAND_ICON[s]}
                        label={t(`observability.command.${s}`)}
                        value={count}
                        hint={s === 'pending' ? t('observability.commandPendingHint') : undefined}
                        level={level}
                      />
                    )
                  })}
                </CardContent>
              </Card>
            </div>
          </div>
        )}
      </AsyncSection>
    </div>
  )
}
