// 系统域展示层小工具：时间格式化与数字格式化，避免各页重复。

/** ISO 时间串 → 本地可读；null / 空 / 非法返回占位。 */
export function formatIso(value: string | null | undefined, fallback = '-'): string {
  if (value === null || value === undefined || value === '') {
    return fallback
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return fallback
  }
  return date.toLocaleString('zh-CN', { hour12: false })
}

/** 千分位数字格式化。 */
export function formatCount(value: number): string {
  return value.toLocaleString('zh-CN')
}

/** 字节 → 可读单位（MB / GB）。 */
export function formatBytes(bytes: number): string {
  const mb = bytes / 1024 / 1024
  if (mb >= 1024) {
    return `${(mb / 1024).toFixed(1)} GB`
  }
  return `${mb.toFixed(0)} MB`
}

/** 秒数 → 「Xd Xh Xm」可读时长。 */
export function formatDuration(seconds: number): string {
  const d = Math.floor(seconds / 86_400)
  const h = Math.floor((seconds % 86_400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const parts: string[] = []
  if (d > 0) {
    parts.push(`${String(d)}d`)
  }
  if (h > 0) {
    parts.push(`${String(h)}h`)
  }
  parts.push(`${String(m)}m`)
  return parts.join(' ')
}
