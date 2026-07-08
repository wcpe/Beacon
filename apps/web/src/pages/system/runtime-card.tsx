// 进程运行时：版本 / 运行时长 / DB 连通 / 在线实例 / 采样器 / 协程 / 堆 / CPU。
// 对齐 B 版——区段图标标题 + 高密度指标磁贴（图标框 + 弱标签 + 大数值），
// DB / 采样器用语义状态药丸，CPU 按占用阈值上色。
import { useTranslation } from 'react-i18next'
import { Activity, Cpu, Database, MemoryStick, Server, Tag, Timer } from 'lucide-react'

import { Badge, SectionHeader, cn, levelText, ratioLevel } from '@beacon/ui'
import type { ReactNode } from 'react'
import type { SystemStatus } from '@beacon/devmock'

import { formatBytes, formatCount, formatDuration } from '../../features/system/format'

// 运行时指标磁贴：图标框 + 弱标签 + 主数值（可含状态药丸）。数值可按等级上色。
function Metric({
  icon,
  label,
  value,
  valueClass,
}: {
  icon: ReactNode
  label: string
  value: ReactNode
  valueClass?: string
}) {
  return (
    <div className="flex flex-col gap-2 rounded-xl border border-border bg-card p-[15px] shadow-card">
      <div className="flex items-center gap-2">
        <span className="grid size-[26px] shrink-0 place-items-center rounded-lg bg-brand-50 text-brand" aria-hidden>
          {icon}
        </span>
        <span className="text-[11.5px] font-medium text-ink-3">{label}</span>
      </div>
      <div className={cn('text-[19px] leading-none font-bold tracking-[-0.3px] text-ink-1 tnum', valueClass)}>
        {value}
      </div>
    </div>
  )
}

export default function RuntimeCard({ status }: { status: SystemStatus }) {
  const { t } = useTranslation()
  const cpuLevel = status.cpuAvailable ? ratioLevel(status.cpuPercent / 100) : 'muted'

  return (
    <section className="grid gap-3">
      <SectionHeader
        icon={<Activity className="size-4" />}
        title={t('system.health.runtime.title')}
      />
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <Metric icon={<Tag className="size-[15px]" />} label={t('system.health.runtime.version')} value={status.version} />
        <Metric
          icon={<Timer className="size-[15px]" />}
          label={t('system.health.runtime.uptime')}
          value={formatDuration(status.uptimeSeconds)}
        />
        <Metric
          icon={<Database className="size-[15px]" />}
          label={t('system.health.runtime.db')}
          value={
            <Badge variant={status.db.connected ? 'ok' : 'crit'} className="gap-1.5">
              <span className="size-1.5 rounded-full bg-current" />
              {status.db.connected
                ? t('system.health.runtime.dbConnected')
                : t('system.health.runtime.dbDisconnected')}
            </Badge>
          }
        />
        <Metric
          icon={<Server className="size-[15px]" />}
          label={t('system.health.runtime.online')}
          value={formatCount(status.onlineInstances)}
        />
        <Metric
          icon={<Activity className="size-[15px]" />}
          label={t('system.health.runtime.sampler')}
          value={
            <Badge variant={status.samplerEnabled ? 'ok' : 'off'} className="gap-1.5">
              <span className="size-1.5 rounded-full bg-current" />
              {status.samplerEnabled ? t('system.health.runtime.samplerOn') : t('system.health.runtime.samplerOff')}
            </Badge>
          }
        />
        <Metric
          icon={<Activity className="size-[15px]" />}
          label={t('system.health.runtime.goroutines')}
          value={formatCount(status.runtime.goroutines)}
        />
        <Metric
          icon={<MemoryStick className="size-[15px]" />}
          label={t('system.health.runtime.heap')}
          value={
            <span className="text-[15px]">
              {formatBytes(status.runtime.heapAlloc)} / {formatBytes(status.runtime.heapSys)}
            </span>
          }
        />
        <Metric
          icon={<Cpu className="size-[15px]" />}
          label={t('system.health.runtime.cpu')}
          value={status.cpuAvailable ? `${status.cpuPercent.toFixed(1)}%` : t('system.health.runtime.cpuUnavailable')}
          valueClass={levelText(cpuLevel)}
        />
      </div>
    </section>
  )
}
