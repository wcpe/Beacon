import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'

import { useControlPlaneResources } from './use-control-plane-resources'
import { useVisibleBusy } from './use-visible-busy'

const KIB = 1024
const MIB = KIB * 1024
const GIB = MIB * 1024

function formatHeapUsage(heapAlloc: number, heapSys: number): string {
  if (!Number.isFinite(heapAlloc) || !Number.isFinite(heapSys) || heapSys <= 0) {
    return '—'
  }

  const units = [
    { divisor: 1, label: 'B' },
    { divisor: KIB, label: 'KB' },
    { divisor: MIB, label: 'MB' },
    { divisor: GIB, label: 'GB' },
  ]
  let unit = units[0]
  for (const candidate of units.slice(1)) {
    if (heapSys >= candidate.divisor) {
      unit = candidate
    }
  }

  const formatValue = (bytes: number) => {
    const value = bytes / unit.divisor
    return value >= 10 || unit.divisor === 1 ? String(Math.round(value)) : value.toFixed(1)
  }
  return `${formatValue(heapAlloc)}/${formatValue(heapSys)} ${unit.label}`
}

function Separator() {
  return <span aria-hidden="true" className="h-4 w-px shrink-0 bg-border" />
}

export default function ControlPlaneResources({
  isRefreshing = false,
}: {
  isRefreshing?: boolean
}) {
  const { t } = useTranslation()
  // isFetching 覆盖「主动刷新 + 8 秒轮询」两种取数；只盯主动刷新会让轮询时的
  // 数字变成硬替换（用户看到数值突然跳变，不知道它刚更新过）。
  const { data: status, isFetching } = useControlPlaneResources()
  // 取数常在个位数毫秒内返回，直接绑 isFetching 会让淡出只存在 5ms（实测），
  // 肉眼不可见；用最短展示时长撑到可感知的窗口。
  const busy = useVisibleBusy(isFetching, isRefreshing)

  const cpuAvailable = status?.cpuAvailable ?? false
  const cpuPercent = status?.cpuPercent ?? 0
  const cpuWarning = cpuAvailable && cpuPercent > 80
  const heapAlloc = status?.runtime.heapAlloc ?? 0
  const heapSys = status?.runtime.heapSys ?? 0
  const heapPercent = heapSys > 0 ? Math.min((heapAlloc / heapSys) * 100, 100) : 0
  const heapWarning = heapSys > 0 && heapPercent > 80
  const cpuBarWidth = cpuAvailable ? Math.min(Math.max(cpuPercent, 0), 100) : 0
  const cpuLabel = cpuAvailable ? `${cpuPercent.toFixed(0)}%` : '—'
  const memoryLabel = formatHeapUsage(heapAlloc, heapSys)
  const groupLabel = t('common.resources.ariaLabel', { cpu: cpuLabel, memory: memoryLabel })

  return (
    <Link
      to="/system"
      aria-label={groupLabel}
      aria-busy={busy ? true : undefined}
      title={t('common.resources.openHealth')}
      data-slot="control-plane-resources"
      className="hidden h-8 w-[min(260px,30vw)] min-w-[200px] shrink-0 cursor-pointer items-center gap-2.5 rounded-lg bg-surface-2/70 px-2 text-[11px] transition-colors hover:bg-surface-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/30 lg:flex"
    >
      <div
        data-slot="resource-cpu"
        data-level={cpuWarning ? 'warning' : 'normal'}
        className={[
          'flex shrink-0 items-center gap-1.5 transition-opacity duration-200 motion-reduce:transition-none',
          busy ? 'opacity-60' : '',
        ].join(' ')}
      >
        <span className="text-ink-3">{t('common.resources.cpu')}</span>
        {/* 迷你条：h-1.5 + w-10 才能看清占比；此前 h-1/w-7 在 23% 时只剩一个小点 */}
        <span
          aria-hidden="true"
          className="h-1.5 w-10 overflow-hidden rounded-full bg-ink-3/25"
          title={cpuAvailable ? `${cpuPercent.toFixed(1)}%` : '—'}
        >
          <span
            className={['block h-full rounded-full transition-[width] duration-500 motion-reduce:transition-none', cpuWarning ? 'bg-warn' : 'bg-brand'].join(' ')}
            style={{ width: `${String(cpuBarWidth)}%` }}
          />
        </span>
        <span className={['min-w-[34px] text-right font-medium tabular-nums', cpuWarning ? 'text-warn' : 'text-ink-1'].join(' ')}>
          {cpuAvailable ? `${cpuPercent.toFixed(0)}%` : '—'}
        </span>
      </div>

      <Separator />

      <div
        data-slot="resource-memory"
        data-level={heapWarning ? 'warning' : 'normal'}
        className={[
          'flex min-w-0 shrink-0 items-center gap-1.5 transition-opacity duration-200 motion-reduce:transition-none',
          busy ? 'opacity-60' : '',
        ].join(' ')}
      >
        <span className="text-ink-3">{t('common.resources.memory')}</span>
        <span className={['whitespace-nowrap font-medium tabular-nums', heapWarning ? 'text-warn' : 'text-ink-1'].join(' ')}>
          {formatHeapUsage(heapAlloc, heapSys)}
        </span>
      </div>

    </Link>
  )
}
