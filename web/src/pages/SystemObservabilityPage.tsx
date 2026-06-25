// 控制面健康页（FR-82）：控制面进程**自身**内部运行态的只读自观测。
// 四组指标——DB 连接池 / 长轮询挂起 / 注册表规模（按健康状态）/ 命令队列深度（按状态）。
// 与 FR-32 可观测看板（agent 网络负载）、FR-73 服务分析（平台运维活动）清晰区分：
// 本页只看 Beacon 自己卡不卡（连接池有没有耗尽、长轮询挂了多少、命令队列堆没堆），只读、不参与决策。

import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Database, Hourglass, Server, ListTodo } from 'lucide-react'
import { systemObservability } from '@/api/client'
import StatCard from './dashboard/StatCard'
import AsyncSection from '@/components/AsyncSection'

// 自观测快照刷新周期（毫秒）：本页打开时短周期轮询，反映控制面当前内部态（不进 FR-33 页眉高频轮询）。
const OBS_REFETCH_MS = 5000

// 健康状态展示顺序（与注册表健康状态机一致：online→degraded→lost→offline）。
const STATUS_ORDER = ['online', 'degraded', 'lost', 'offline'] as const
// 命令队列状态展示顺序（与 agent_command 状态机一致）。
const COMMAND_ORDER = ['pending', 'fetched', 'ready', 'done', 'failed', 'expired'] as const

export default function SystemObservabilityPage() {
  const { t } = useTranslation()
  const { data, isLoading, isError, error, isFetching } = useQuery({
    queryKey: ['system-observability'],
    queryFn: systemObservability,
    refetchInterval: OBS_REFETCH_MS,
  })

  return (
    <div className="space-y-4">
      {/* 折叠进设置子 tab 后页标题由子 tab 标签承担（FR-95），此处仅留副标题 + 刷新指示 */}
      <div className="flex items-center gap-3">
        <p className="text-sm text-muted-foreground">{t('observability.subtitle')}</p>
        {isFetching && <span className="text-sm text-muted-foreground">{t('common.refreshing')}</span>}
      </div>

      <AsyncSection isLoading={isLoading} isError={isError} error={error}>
        {data && (
          <div className="space-y-6">
            {/* ===== 数据库连接池 ===== */}
            <section className="space-y-3">
              <h2 className="text-lg font-semibold">{t('observability.dbPoolTitle')}</h2>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
                <StatCard
                  label={t('observability.dbOpen')}
                  value={data.dbPool.openConnections}
                  hint={t('observability.dbMax', {
                    // maxOpenConnections=0 表示无限（database/sql 约定）
                    max: data.dbPool.maxOpenConnections > 0 ? data.dbPool.maxOpenConnections : '∞',
                  })}
                  icon={<Database className="size-4" />}
                />
                <StatCard
                  label={t('observability.dbInUseIdle')}
                  value={`${data.dbPool.inUse} / ${data.dbPool.idle}`}
                  hint={t('observability.dbInUseIdleHint')}
                  icon={<Database className="size-4" />}
                />
                <StatCard
                  label={t('observability.dbWait')}
                  value={data.dbPool.waitCount}
                  hint={t('observability.dbWaitHint', { ms: data.dbPool.waitDurationMs })}
                  icon={<Database className="size-4" />}
                />
              </div>
            </section>

            {/* ===== 长轮询挂起 ===== */}
            <section className="space-y-3">
              <h2 className="text-lg font-semibold">{t('observability.longpollTitle')}</h2>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
                <StatCard
                  label={t('observability.longpollTotal')}
                  value={data.longpoll.total}
                  hint={t('observability.longpollTotalHint')}
                  icon={<Hourglass className="size-4" />}
                />
                <StatCard
                  label={t('observability.longpollByChannel')}
                  value={`${data.longpoll.config} / ${data.longpoll.file} / ${data.longpoll.topology} / ${data.longpoll.command}`}
                  hint={t('observability.longpollByChannelHint')}
                  icon={<Hourglass className="size-4" />}
                />
              </div>
            </section>

            {/* ===== 注册表规模 ===== */}
            <section className="space-y-3">
              <h2 className="text-lg font-semibold">{t('observability.registryTitle')}</h2>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
                <StatCard
                  label={t('observability.registryTotal')}
                  value={data.registryTotal}
                  hint={t('observability.registryTotalHint')}
                  icon={<Server className="size-4" />}
                />
                <StatCard
                  label={t('observability.registryByStatus')}
                  // 按状态机顺序拼出「online N · degraded N …」，仅展示有计数的状态
                  value={
                    STATUS_ORDER.filter((s) => (data.registryByStatus[s] ?? 0) > 0)
                      .map((s) => `${t(`observability.status.${s}`)} ${data.registryByStatus[s]}`)
                      .join(' · ') || t('observability.none')
                  }
                  icon={<Server className="size-4" />}
                />
              </div>
            </section>

            {/* ===== 命令队列深度 ===== */}
            <section className="space-y-3">
              <h2 className="text-lg font-semibold">{t('observability.commandTitle')}</h2>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
                <StatCard
                  label={t('observability.commandPending')}
                  value={data.commandByStatus['pending'] ?? 0}
                  hint={t('observability.commandPendingHint')}
                  icon={<ListTodo className="size-4" />}
                />
                <StatCard
                  label={t('observability.commandByStatus')}
                  // 按命令状态机顺序拼出有计数的状态
                  value={
                    COMMAND_ORDER.filter((s) => (data.commandByStatus[s] ?? 0) > 0)
                      .map((s) => `${t(`observability.command.${s}`)} ${data.commandByStatus[s]}`)
                      .join(' · ') || t('observability.none')
                  }
                  icon={<ListTodo className="size-4" />}
                />
              </div>
            </section>
          </div>
        )}
      </AsyncSection>
    </div>
  )
}
