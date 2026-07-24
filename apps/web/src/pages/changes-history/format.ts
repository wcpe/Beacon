// 交付历史页时间格式化：ISO 转本地可读串，缺省返回占位。
export function formatTime(iso: string | null): string {
  if (iso === null || iso === '') {
    return '-'
  }
  const date = new Date(iso)
  return Number.isNaN(date.getTime()) ? iso : date.toLocaleString()
}
