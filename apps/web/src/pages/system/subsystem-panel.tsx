// 子系统健康面板：连接池 / 长轮询 / 注册表 / 命令队列的仪表环总览 + 逐项明细。
// 对齐 B 版——区段图标标题 + 仪表环卡 + 高密度明细网格（指标磁贴 / 状态药丸），弱化裸表格。
import { useTranslation } from 'react-i18next'
import { Database, Gauge, ListChecks, Radio, ServerCog, SquareTerminal } from 'lucide-react'

import { Badge, GaugeRing, SectionHeader, cn, ratioLevel, statusLevel } from '@beacon/ui'
import type { HealthLevel } from '@beacon/ui'
import type { ReactNode } from 'react'
import type { SystemObservability } from '@beacon/devmock'

import { formatCount } from '../../features/system/format'

// 明细指标格：弱标签 + 等宽数值（可上语义色）。
function Stat({ label, value, tone }: { label: string; value: ReactNode; tone?: 'warn' }) {
  return (
    <div className="flex flex-col gap-0.5 rounded-lg bg-surface-2 px-3 py-2">
      <span className="text-[11px] leading-none text-ink-4">{label}</span>
      <span className={cn('text-[15px] font-semibold leading-none tnum text-ink-1', tone === 'warn' && 'text-warn')}>
        {value}
      </span>
    </div>
  )
}

// 计数明细卡壳：图标标题 + 内容。
function DetailCard({ icon, title, children }: { icon: ReactNode; title: string; children: ReactNode }) {
  return (
    <div className="grid gap-2.5 rounded-xl border border-border bg-card p-4 shadow-card">
      <div className="flex items-center gap-2">
        <span className="grid size-[26px] shrink-0 place-items-center rounded-lg bg-brand-50 text-brand" aria-hidden>
          {icon}
        </span>
        <h3 className="text-[13px] font-semibold text-ink-1">{title}</h3>
      </div>
      {children}
    </div>
  )
}

// 状态计数明细：把 Record<状态, 数量> 渲染为一排语义状态药丸（在线绿 / 失联红 / 其他）。
function StatusCounts({ counts }: { counts: Record<string, number> }) {
  const { t } = useTranslation()
  const entries = Object.entries(counts)
  if (entries.length === 0) {
    return <p className="text-sm text-ink-4">{t('system.health.detail.emptyCounts')}</p>
  }
  return (
    <div className="flex flex-wrap gap-2">
      {entries.map(([status, count]) => {
        const level = statusLevel(status)
        const variant = STATUS_VARIANT[level]
        return (
          <Badge key={status} variant={variant} className="gap-1.5">
            <span className="size-1.5 rounded-full bg-current" />
            <span className="font-mono">{status}</span>
            <span className="tnum">{formatCount(count)}</span>
          </Badge>
        )
      })}
    </div>
  )
}

// 健康等级 → 状态药丸变体。
const STATUS_VARIANT: Record<HealthLevel, 'ok' | 'warn' | 'crit' | 'off'> = {
  ok: 'ok',
  warn: 'warn',
  danger: 'crit',
  muted: 'off',
}

export default function SubsystemPanel({ observability }: { observability: SystemObservability }) {
  const { t } = useTranslation()
  const { dbPool, longpoll, registryByStatus, registryTotal, commandByStatus } = observability

  const poolRatio = dbPool.maxOpenConnections === 0 ? 0 : dbPool.inUse / dbPool.maxOpenConnections
  // Record 索引在本工程默认返回 number（未开 noUncheckedIndexedAccess），缺键回退 0
  const commandFailed = (commandByStatus.failed as number | undefined) ?? 0

  return (
    <div className="grid gap-6">
      {/* 子系统仪表环总览 */}
      <section className="grid gap-3">
        <SectionHeader icon={<Gauge className="size-4" />} title={t('system.health.subsystems.title')} />
        <div className="grid gap-3.5 rounded-xl border border-border bg-card p-5 shadow-card sm:grid-cols-2 lg:grid-cols-4">
          <GaugeRing
            icon={<Database className="size-5" />}
            ratio={poolRatio}
            level={ratioLevel(poolRatio)}
            label={t('system.health.subsystems.dbPool')}
            valueText={t('system.health.subsystems.dbPoolValue', {
              inUse: dbPool.inUse,
              max: dbPool.maxOpenConnections,
            })}
          />
          <GaugeRing
            icon={<Radio className="size-5" />}
            ratio={null}
            level="ok"
            label={t('system.health.subsystems.longpoll')}
            valueText={formatCount(longpoll.total)}
          />
          <GaugeRing
            icon={<ServerCog className="size-5" />}
            ratio={null}
            level="ok"
            label={t('system.health.subsystems.registry')}
            valueText={formatCount(registryTotal)}
          />
          <GaugeRing
            icon={<SquareTerminal className="size-5" />}
            ratio={null}
            level={commandFailed > 0 ? 'warn' : 'ok'}
            label={t('system.health.subsystems.command')}
            valueText={formatCount(commandFailed)}
          />
        </div>
      </section>

      {/* 明细区：连接池 / 长轮询 / 注册表 / 命令队列 */}
      <section className="grid gap-3">
        <SectionHeader icon={<ListChecks className="size-4" />} title={t('system.health.detail.title')} />
        <div className="grid gap-3.5 lg:grid-cols-2">
          <DetailCard icon={<Database className="size-[15px]" />} title={t('system.health.detail.dbPoolTitle')}>
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
              <Stat label={t('system.health.detail.maxOpen')} value={formatCount(dbPool.maxOpenConnections)} />
              <Stat label={t('system.health.detail.open')} value={formatCount(dbPool.openConnections)} />
              <Stat label={t('system.health.detail.inUse')} value={formatCount(dbPool.inUse)} />
              <Stat label={t('system.health.detail.idle')} value={formatCount(dbPool.idle)} />
              <Stat
                label={t('system.health.detail.waitCount')}
                value={formatCount(dbPool.waitCount)}
                tone={dbPool.waitCount > 0 ? 'warn' : undefined}
              />
              <Stat label={t('system.health.detail.waitDuration')} value={formatCount(dbPool.waitDurationMs)} />
            </div>
          </DetailCard>

          <DetailCard icon={<Radio className="size-[15px]" />} title={t('system.health.detail.longpollTitle')}>
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
              <Stat label={t('system.health.detail.config')} value={formatCount(longpoll.config)} />
              <Stat label={t('system.health.detail.file')} value={formatCount(longpoll.file)} />
              <Stat label={t('system.health.detail.topology')} value={formatCount(longpoll.topology)} />
              <Stat label={t('system.health.detail.commandLp')} value={formatCount(longpoll.command)} />
              <Stat label={t('system.health.detail.total')} value={formatCount(longpoll.total)} />
            </div>
          </DetailCard>

          <DetailCard icon={<ServerCog className="size-[15px]" />} title={t('system.health.detail.registryTitle')}>
            <StatusCounts counts={registryByStatus} />
          </DetailCard>

          <DetailCard icon={<SquareTerminal className="size-[15px]" />} title={t('system.health.detail.commandTitle')}>
            <StatusCounts counts={commandByStatus} />
          </DetailCard>
        </div>
      </section>
    </div>
  )
}
