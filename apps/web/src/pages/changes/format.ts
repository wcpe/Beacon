// 变更单页展示格式化：字节数人类可读、时间本地化。

/** 字节数转人类可读（B/KB/MB/GB） */
export function formatBytes(size: number): string {
  if (size < 1024) {
    return `${String(size)} B`
  }
  const units = ['KB', 'MB', 'GB', 'TB']
  let value = size / 1024
  let unitIndex = 0
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024
    unitIndex += 1
  }
  return `${value.toFixed(1)} ${units[unitIndex]}`
}

/** ISO 时间转本地可读串（缺省返回占位） */
export function formatTime(iso: string | null): string {
  if (iso === null || iso === '') {
    return '-'
  }
  const date = new Date(iso)
  return Number.isNaN(date.getTime()) ? iso : date.toLocaleString()
}
