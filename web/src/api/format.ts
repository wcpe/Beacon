// 通用展示格式化工具。

// 把后端 UTC 时间字符串格式化为本地可读时间；无效或空值原样回退。
export function formatTime(iso: string | undefined | null): string {
  if (!iso) return '-'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString()
}

// 把秒数格式化为人类可读运行时长（天/时/分/秒，最多取两个量级）。
// 负数或非有限值回退为 '-'；0 显示 '0 秒'。
export function formatDuration(seconds: number | undefined | null): string {
  if (seconds === undefined || seconds === null || !Number.isFinite(seconds) || seconds < 0) return '-'
  if (seconds < 1) return '0 秒'
  const total = Math.floor(seconds)
  const days = Math.floor(total / 86400)
  const hours = Math.floor((total % 86400) / 3600)
  const mins = Math.floor((total % 3600) / 60)
  const secs = total % 60
  const parts: string[] = []
  if (days > 0) parts.push(`${days} 天`)
  if (hours > 0) parts.push(`${hours} 小时`)
  if (mins > 0) parts.push(`${mins} 分`)
  if (secs > 0) parts.push(`${secs} 秒`)
  // 只取最高的两个量级，避免「2 天 3 小时 5 分 7 秒」过长
  return parts.slice(0, 2).join(' ')
}

// 把字节数格式化为人类可读形式（B/KB/MB/GB/TB，1024 进制）。
// 负数或非有限值回退为 '-'；0 显示 '0 B'。
export function formatBytes(bytes: number | undefined | null): string {
  if (bytes === undefined || bytes === null || !Number.isFinite(bytes) || bytes < 0) return '-'
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const exp = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const value = bytes / Math.pow(1024, exp)
  // 整数省略小数，否则保留一位小数
  const text = Number.isInteger(value) ? String(value) : value.toFixed(1)
  return `${text} ${units[exp]}`
}
