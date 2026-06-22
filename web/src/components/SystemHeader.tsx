// 控制面自身状态条（FR-33）：位于右侧主内容列顶部（侧边栏右侧），展示控制面进程本身的健康。
// 区别于可观测看板（FR-32）的 agent 网络聚合指标——这里是版本 / 运行时长 / DB 连通 /
// 在线实例数 / 采样器状态 + Go 运行时资源（goroutine / 堆内存）+ 进程 CPU%（gopsutil 采集，
// 采集失败时降级为「不可用」）。

import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { systemStatus } from '@/api/client'
import { formatBytes, formatDuration } from '@/api/format'
import { cn } from '@/lib/utils'

// 自身状态刷新周期（毫秒）：短周期以实时反映控制面健康（含 DB 断开）
const STATUS_REFETCH_MS = 5000

// 单个指标项：标签 + 值（值区可带前缀小圆点表示连通态）
function StatItem({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col">
      <span className="text-[11px] leading-none text-muted-foreground">{label}</span>
      <span className="mt-0.5 text-sm font-medium tabular-nums">{children}</span>
    </div>
  )
}

export default function SystemHeader() {
  const { t } = useTranslation()
  const { data, isError } = useQuery({
    queryKey: ['system-status'],
    queryFn: systemStatus,
    refetchInterval: STATUS_REFETCH_MS,
  })

  // 拉取失败（含网络断开）也视作不健康：DB 点显示红、其余字段占位
  const dbConnected = !isError && (data?.db.connected ?? false)

  return (
    <header className="flex shrink-0 flex-wrap items-center gap-x-8 gap-y-3 border-b bg-background px-6 py-2.5">
      <div className="flex items-center gap-2">
        <span className="text-sm font-semibold">{t('systemHeader.title')}</span>
        <span className="rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">
          {data?.version ?? '-'}
        </span>
      </div>

      <StatItem label={t('systemHeader.database')}>
        <span className="inline-flex items-center gap-1.5">
          <span
            aria-hidden
            className={cn('inline-block h-2 w-2 rounded-full', dbConnected ? 'bg-green-600' : 'bg-red-600')}
          />
          {isError
            ? t('systemHeader.unreachable')
            : dbConnected
              ? t('systemHeader.connected')
              : t('systemHeader.disconnected')}
        </span>
      </StatItem>

      <StatItem label={t('systemHeader.uptime')}>{formatDuration(data?.uptimeSeconds)}</StatItem>

      <StatItem label={t('systemHeader.onlineInstances')}>{data?.onlineInstances ?? '-'}</StatItem>

      <StatItem label={t('systemHeader.sampler')}>
        {data ? (data.samplerEnabled ? t('systemHeader.samplerEnabled') : t('systemHeader.samplerDisabled')) : '-'}
      </StatItem>

      <StatItem label={t('systemHeader.goroutine')}>{data?.runtime.goroutines ?? '-'}</StatItem>

      <StatItem label={t('systemHeader.goHeap')}>
        {data ? `${formatBytes(data.runtime.heapAlloc)} / ${formatBytes(data.runtime.heapSys)}` : '-'}
      </StatItem>

      <StatItem label={t('systemHeader.processCpu')}>
        {data?.cpuAvailable ? `${data.cpuPercent.toFixed(1)}%` : t('systemHeader.unavailable')}
      </StatItem>
    </header>
  )
}
