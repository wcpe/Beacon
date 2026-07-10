// 子系统健康面板：上方仪表环概览 + 下方子系统列表主列（点行 → 右侧非模态详情面板）。
// 点子系统看该子系统明细指标 / 队列 / 连接池明细，取代原并列长表堆叠。
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Database, Gauge, ListChecks, Radio, ServerCog, SquareTerminal } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'

import { Badge, GaugeRing, SectionHeader, cn, ratioLevel, statusLevel } from '@beacon/ui'
import type { HealthLevel } from '@beacon/ui'
import type { ReactNode } from 'react'
import type { SystemObservability } from '@beacon/devmock'

import MasterDetail from '../../features/observability/master-detail'
import { formatCount } from '../../features/system/format'

// 健康等级 → 状态药丸变体。
const STATUS_VARIANT: Record<HealthLevel, 'ok' | 'warn' | 'crit' | 'off'> = {
  ok: 'ok',
  warn: 'warn',
  danger: 'crit',
  muted: 'off',
}

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

// 状态计数明细：把 Record<状态, 数量> 渲染为一排语义状态药丸（在线绿 / 失联红 / 其他）。
function StatusCounts({ counts, emptyText }: { counts: Record<string, number>; emptyText: string }) {
  const entries = Object.entries(counts)
  if (entries.length === 0) {
    return <p className="text-sm text-ink-4">{emptyText}</p>
  }
  return (
    <div className="flex flex-wrap gap-2">
      {entries.map(([status, count]) => (
        <Badge key={status} variant={STATUS_VARIANT[statusLevel(status)]} className="gap-1.5">
          <span className="size-1.5 rounded-full bg-current" />
          <span className="font-mono">{status}</span>
          <span className="tnum">{formatCount(count)}</span>
        </Badge>
      ))}
    </div>
  )
}

// 子系统描述：行内前置（名称 / 状态药丸 / 关键指标）+ 详情内容。
interface Subsystem {
  key: string
  icon: LucideIcon
  name: string
  detailTitle: string
  level: HealthLevel
  metric: string
  detail: ReactNode
}

export default function SubsystemPanel({ observability }: { observability: SystemObservability }) {
  const { t } = useTranslation()
  const { dbPool, longpoll, registryByStatus, registryTotal, commandByStatus } = observability
  const [selectedKey, setSelectedKey] = useState<string | null>(null)

  const poolRatio = dbPool.maxOpenConnections === 0 ? 0 : dbPool.inUse / dbPool.maxOpenConnections
  // Record 索引在本工程默认返回 number（未开 noUncheckedIndexedAccess），缺键回退 0
  const commandFailed = (commandByStatus.failed as number | undefined) ?? 0
  const emptyCounts = t('system.health.detail.emptyCounts')

  const subsystems = useMemo<Subsystem[]>(
    () => [
      {
        key: 'dbPool',
        icon: Database,
        name: t('system.health.subsystems.dbPool'),
        detailTitle: t('system.health.detail.dbPoolTitle'),
        level: ratioLevel(poolRatio),
        metric: t('system.health.subsystems.dbPoolValue', { inUse: dbPool.inUse, max: dbPool.maxOpenConnections }),
        detail: (
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
        ),
      },
      {
        key: 'longpoll',
        icon: Radio,
        name: t('system.health.subsystems.longpoll'),
        detailTitle: t('system.health.detail.longpollTitle'),
        level: 'ok',
        metric: formatCount(longpoll.total),
        detail: (
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
            <Stat label={t('system.health.detail.config')} value={formatCount(longpoll.config)} />
            <Stat label={t('system.health.detail.file')} value={formatCount(longpoll.file)} />
            <Stat label={t('system.health.detail.topology')} value={formatCount(longpoll.topology)} />
            <Stat label={t('system.health.detail.commandLp')} value={formatCount(longpoll.command)} />
            <Stat label={t('system.health.detail.total')} value={formatCount(longpoll.total)} />
          </div>
        ),
      },
      {
        key: 'registry',
        icon: ServerCog,
        name: t('system.health.subsystems.registry'),
        detailTitle: t('system.health.detail.registryTitle'),
        level: 'ok',
        metric: formatCount(registryTotal),
        detail: <StatusCounts counts={registryByStatus} emptyText={emptyCounts} />,
      },
      {
        key: 'command',
        icon: SquareTerminal,
        name: t('system.health.subsystems.command'),
        detailTitle: t('system.health.detail.commandTitle'),
        level: commandFailed > 0 ? 'warn' : 'ok',
        metric: formatCount(commandFailed),
        detail: <StatusCounts counts={commandByStatus} emptyText={emptyCounts} />,
      },
    ],
    [t, dbPool, longpoll, registryByStatus, registryTotal, commandByStatus, commandFailed, poolRatio, emptyCounts],
  )

  const selected = subsystems.find((s) => s.key === selectedKey) ?? null

  const master = (
    <div className="grid gap-3.5">
      {/* 子系统仪表环概览 */}
      <div className="grid gap-3.5 rounded-xl border border-border bg-card p-5 shadow-card sm:grid-cols-2 lg:grid-cols-4">
        <GaugeRing
          icon={<Database className="size-5" />}
          ratio={poolRatio}
          level={ratioLevel(poolRatio)}
          label={t('system.health.subsystems.dbPool')}
          valueText={t('system.health.subsystems.dbPoolValue', { inUse: dbPool.inUse, max: dbPool.maxOpenConnections })}
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

      {/* 子系统列表：行内前置名称 / 状态药丸 / 关键指标，点行看明细 */}
      <div className="grid gap-2">
        <SectionHeader icon={<ListChecks className="size-4" />} title={t('system.health.subsystems.listTitle')} />
        <div className="grid gap-2 rounded-xl border border-border bg-card p-2 shadow-card">
          {subsystems.map((s) => {
            const Icon = s.icon
            const on = s.key === selectedKey
            return (
              <button
                key={s.key}
                type="button"
                aria-current={on ? 'true' : undefined}
                onClick={() => {
                  setSelectedKey(s.key)
                }}
                className={cn(
                  'flex items-center gap-3 rounded-lg px-3 py-2.5 text-left transition-colors',
                  on ? 'bg-brand-50' : 'hover:bg-surface-2',
                )}
              >
                <span className="grid size-[26px] shrink-0 place-items-center rounded-lg bg-brand-50 text-brand" aria-hidden>
                  <Icon className="size-[15px]" />
                </span>
                <span className="text-[13px] font-medium text-ink-1">{s.name}</span>
                <Badge variant={STATUS_VARIANT[s.level]} className="gap-1.5">
                  <span className="size-1.5 rounded-full bg-current" />
                  {t(`system.health.level.${s.level}`)}
                </Badge>
                <span className="ml-auto text-[13px] font-semibold tnum text-ink-2">{s.metric}</span>
              </button>
            )
          })}
        </div>
      </div>
    </div>
  )

  return (
    <section className="grid gap-3">
      <SectionHeader icon={<Gauge className="size-4" />} title={t('system.health.subsystems.title')} />
      <MasterDetail
        master={master}
        detail={selected ? selected.detail : null}
        detailTitle={selected?.detailTitle}
        closeLabel={t('system.common.close')}
        onClose={() => {
          setSelectedKey(null)
        }}
      />
    </section>
  )
}
