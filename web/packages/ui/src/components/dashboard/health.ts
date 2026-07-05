// 看板 / 健康页共用的健康色与阈值工具（统一设计语言，避免各处重复硬编码语义色）。
// 状态色一律走 Tailwind 语义色：在线/正常=green、亚健康/注意=amber、失联/离线/错误=red、BC/次要=teal/indigo。
// 仅用语义类，不硬编码品牌色；与 SystemHeader 连接态药丸同口径（bg-<色>-600/10 text-<色>-700 dark:text-<色>-400）。

// 健康等级：ok=正常 / warn=注意 / danger=危险 / muted=无意义（如离线、无样本）。
export type HealthLevel = 'ok' | 'warn' | 'danger' | 'muted'

// 等级 → 主色（实心点 / 进度环描边 / 左侧色条用）。语义类，dark 自适配。
const LEVEL_SOLID: Record<HealthLevel, string> = {
  ok: 'bg-green-600',
  warn: 'bg-amber-500',
  danger: 'bg-red-600',
  muted: 'bg-muted-foreground/40',
}

// 等级 → 文本色（数值按阈值上色用）。
const LEVEL_TEXT: Record<HealthLevel, string> = {
  ok: 'text-green-700 dark:text-green-400',
  warn: 'text-amber-600 dark:text-amber-400',
  danger: 'text-red-700 dark:text-red-400',
  muted: 'text-muted-foreground',
}

// 等级 → 药丸底色（浅底 + 同色字，柔和不刺眼）。
const LEVEL_SOFT: Record<HealthLevel, string> = {
  ok: 'bg-green-600/10 text-green-700 dark:text-green-400',
  warn: 'bg-amber-500/10 text-amber-600 dark:text-amber-400',
  danger: 'bg-red-600/10 text-red-700 dark:text-red-400',
  muted: 'bg-muted text-muted-foreground',
}

// 等级 → 进度环描边的实际颜色值（conic-gradient 需具体颜色，故映射到 Tailwind 调色板的等价值）。
// 取自 Tailwind 默认调色板（green-600 / amber-500 / red-600 / 中性灰），与上面语义类肉眼一致。
const LEVEL_RING: Record<HealthLevel, string> = {
  ok: '#16a34a',
  warn: '#f59e0b',
  danger: '#dc2626',
  muted: '#9ca3af',
}

// 取等级对应的实心色类（点 / 色条）。
export function levelSolid(level: HealthLevel): string {
  return LEVEL_SOLID[level]
}

// 取等级对应的文本色类（数值上色）。
export function levelText(level: HealthLevel): string {
  return LEVEL_TEXT[level]
}

// 取等级对应的药丸底色类。
export function levelSoft(level: HealthLevel): string {
  return LEVEL_SOFT[level]
}

// 取等级对应的进度环颜色值（具体颜色，供 conic-gradient）。
export function levelRing(level: HealthLevel): string {
  return LEVEL_RING[level]
}

// 实例健康状态 → 健康等级：online=正常、lost/offline=危险/离线、degraded=注意、其余未知按注意。
export function statusLevel(status: string): HealthLevel {
  switch (status) {
    case 'online':
      return 'ok'
    case 'degraded':
      return 'warn'
    case 'lost':
      return 'danger'
    case 'offline':
      return 'muted'
    default:
      return 'warn'
  }
}

// 占比（0~1）按双阈值定级：< warnAt 正常、[warnAt, dangerAt) 注意、>= dangerAt 危险。
// 用于连接池占用率、CPU、内存等「越高越吃紧」的比率指标。
export function ratioLevel(ratio: number, warnAt = 0.7, dangerAt = 0.9): HealthLevel {
  if (!Number.isFinite(ratio) || ratio < 0) return 'muted'
  if (ratio >= dangerAt) return 'danger'
  if (ratio >= warnAt) return 'warn'
  return 'ok'
}

// 计数阈值定级：0 正常、(0, dangerAt) 注意、>= dangerAt 危险。
// 用于「出现即需关注」的计数（如连接池等待次数、失联实例数、命令队列失败数）。
export function countLevel(count: number, dangerAt = Infinity): HealthLevel {
  if (!Number.isFinite(count) || count <= 0) return 'ok'
  if (count >= dangerAt) return 'danger'
  return 'warn'
}
