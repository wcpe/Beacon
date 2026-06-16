// 通用展示格式化工具。

// 把后端 UTC 时间字符串格式化为本地可读时间；无效或空值原样回退。
export function formatTime(iso: string | undefined | null): string {
  if (!iso) return '-'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString()
}
