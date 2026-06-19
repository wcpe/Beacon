// 通用展示格式化工具。

// 把后端 UTC 时间字符串格式化为本地可读时间；无效或空值原样回退。
export function formatTime(iso: string | undefined | null): string {
  if (!iso) return '-'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString()
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
