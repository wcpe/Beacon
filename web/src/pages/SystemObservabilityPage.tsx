// 控制面健康页（FR-82）：控制面进程**自身**内部运行态的只读自观测（ADR-0048 恢复为独立页 /system）。
// 四组指标——DB 连接池 / 长轮询挂起 / 注册表规模（按健康状态）/ 命令队列深度（按状态）——
// 以详细分区表格呈现（不只几个大数字），让运维能看清队列 / 连接池 / 挂起的逐项明细。
// 与 FR-32 可观测看板（agent 网络负载）、FR-73 服务分析（平台运维活动）清晰区分：
// 本页只看 Beacon 自己卡不卡（连接池有没有耗尽、长轮询挂了多少、命令队列堆没堆），只读、不参与决策。

import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { systemObservability } from '@/api/client'
import AsyncSection from '@/components/AsyncSection'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'

// 自观测快照刷新周期（毫秒）：本页打开时短周期轮询，反映控制面当前内部态（不进 FR-33 页眉高频轮询）。
const OBS_REFETCH_MS = 5000

// 健康状态展示顺序（与注册表健康状态机一致：online→degraded→lost→offline）。
const STATUS_ORDER = ['online', 'degraded', 'lost', 'offline'] as const
// 命令队列状态展示顺序（与 agent_command 状态机一致）。
const COMMAND_ORDER = ['pending', 'fetched', 'ready', 'done', 'failed', 'expired'] as const

// 详细明细一行：标签 + 值 + 说明（说明可选）。值用等宽数字便于纵向对齐。
function MetricRow({ label, value, hint }: { label: string; value: React.ReactNode; hint?: string }) {
  return (
    <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 py-2">
      <div className="min-w-0">
        <div className="text-sm">{label}</div>
        {hint && <div className="mt-0.5 text-xs text-muted-foreground">{hint}</div>}
      </div>
      <div className="text-sm font-medium tabular-nums">{value}</div>
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
          <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
            {/* ===== 数据库连接池：逐项明细 ===== */}
            <Card>
              <CardHeader>
                <CardTitle className="text-base">{t('observability.dbPoolTitle')}</CardTitle>
              </CardHeader>
              <CardContent className="divide-y py-0">
                <MetricRow
                  label={t('observability.dbOpen')}
                  value={data.dbPool.openConnections}
                  hint={t('observability.dbOpenHint')}
                />
                <MetricRow
                  label={t('observability.dbMaxLabel')}
                  // maxOpenConnections=0 表示无限（database/sql 约定）
                  value={data.dbPool.maxOpenConnections > 0 ? data.dbPool.maxOpenConnections : '∞'}
                  hint={t('observability.dbMaxHint')}
                />
                <MetricRow label={t('observability.dbInUse')} value={data.dbPool.inUse} hint={t('observability.dbInUseHint')} />
                <MetricRow label={t('observability.dbIdle')} value={data.dbPool.idle} hint={t('observability.dbIdleHint')} />
                <MetricRow
                  label={t('observability.dbWait')}
                  value={data.dbPool.waitCount}
                  hint={t('observability.dbWaitCountHint')}
                />
                <MetricRow
                  label={t('observability.dbWaitDuration')}
                  value={`${data.dbPool.waitDurationMs} ms`}
                  hint={t('observability.dbWaitDurationHint')}
                />
              </CardContent>
            </Card>

            {/* ===== 长轮询挂起：四通道逐项明细 ===== */}
            <Card>
              <CardHeader>
                <CardTitle className="text-base">{t('observability.longpollTitle')}</CardTitle>
              </CardHeader>
              <CardContent className="divide-y py-0">
                <MetricRow
                  label={t('observability.longpollTotal')}
                  value={data.longpoll.total}
                  hint={t('observability.longpollTotalHint')}
                />
                <MetricRow label={t('observability.longpollConfig')} value={data.longpoll.config} />
                <MetricRow label={t('observability.longpollFile')} value={data.longpoll.file} />
                <MetricRow label={t('observability.longpollTopology')} value={data.longpoll.topology} />
                <MetricRow label={t('observability.longpollCommand')} value={data.longpoll.command} />
              </CardContent>
            </Card>

            {/* ===== 注册表规模：总数 + 按健康状态逐项 ===== */}
            <Card>
              <CardHeader>
                <CardTitle className="text-base">{t('observability.registryTitle')}</CardTitle>
              </CardHeader>
              <CardContent className="divide-y py-0">
                <MetricRow
                  label={t('observability.registryTotal')}
                  value={data.registryTotal}
                  hint={t('observability.registryTotalHint')}
                />
                {STATUS_ORDER.map((s) => (
                  <MetricRow
                    key={s}
                    label={t(`observability.status.${s}`)}
                    value={data.registryByStatus[s] ?? 0}
                  />
                ))}
              </CardContent>
            </Card>

            {/* ===== 命令队列深度：按状态逐项明细 ===== */}
            <Card>
              <CardHeader>
                <CardTitle className="text-base">{t('observability.commandTitle')}</CardTitle>
              </CardHeader>
              <CardContent className="divide-y py-0">
                {COMMAND_ORDER.map((s) => (
                  <MetricRow
                    key={s}
                    label={t(`observability.command.${s}`)}
                    value={data.commandByStatus[s] ?? 0}
                    hint={s === 'pending' ? t('observability.commandPendingHint') : undefined}
                  />
                ))}
              </CardContent>
            </Card>
          </div>
        )}
      </AsyncSection>
    </div>
  )
}
