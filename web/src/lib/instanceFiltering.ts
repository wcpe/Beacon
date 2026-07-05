import type { FileSyncTargetStatus, FileSyncTargetView, InstanceView } from '@/api/types'

const STATUS_WEIGHT: Record<string, number> = {
  lost: 0,
  degraded: 1,
  offline: 2,
  online: 3,
}

export interface VisibleWindow<T> {
  items: T[]
  total: number
  hidden: number
}

function includes(text: string | null | undefined, keyword: string): boolean {
  return (text ?? '').toLowerCase().includes(keyword)
}

export function instanceMatchesKeyword(instance: InstanceView, rawKeyword: string): boolean {
  const keyword = rawKeyword.trim().toLowerCase()
  if (!keyword) return true
  return (
    includes(instance.serverId, keyword) ||
    includes(instance.address, keyword) ||
    includes(instance.namespace, keyword) ||
    includes(instance.group, keyword) ||
    includes(instance.zone, keyword)
  )
}

export function filterInstancesByKeyword(
  instances: InstanceView[],
  keyword: string,
): InstanceView[] {
  const query = keyword.trim()
  if (!query) return instances
  return instances.filter((instance) => instanceMatchesKeyword(instance, query))
}

export function prioritizeInstances(instances: InstanceView[]): InstanceView[] {
  return [...instances].sort((a, b) => {
    const aw = STATUS_WEIGHT[a.status] ?? 4
    const bw = STATUS_WEIGHT[b.status] ?? 4
    if (aw !== bw) return aw - bw
    return a.serverId.localeCompare(b.serverId)
  })
}

export function getVisibleWindow<T>(items: T[], limit: number): VisibleWindow<T> {
  const size = Math.max(1, limit)
  const total = items.length
  const visible = items.slice(0, size)
  return {
    items: visible,
    total,
    hidden: Math.max(0, total - visible.length),
  }
}

export function mergeSelectedIds(current: string[], ids: Iterable<string>): string[] {
  const next = new Set(current)
  for (const id of ids) next.add(id)
  return [...next]
}

export function removeSelectedIds(current: string[], ids: Iterable<string>): string[] {
  const removed = new Set(ids)
  return current.filter((id) => !removed.has(id))
}

export function filterFileSyncTargets(
  targets: FileSyncTargetView[],
  options: {
    keyword?: string
    status?: FileSyncTargetStatus | 'all'
    failedFirst?: boolean
  },
): FileSyncTargetView[] {
  const keyword = (options.keyword ?? '').trim().toLowerCase()
  const status = options.status ?? 'all'
  const filtered = targets.filter((target) => {
    const keywordMatched = !keyword || includes(target.serverId, keyword)
    const statusMatched = status === 'all' || target.status === status
    return keywordMatched && statusMatched
  })
  if (!options.failedFirst) return filtered
  return filtered
    .map((target, index) => ({ target, index }))
    .sort((a, b) => {
      const af = a.target.status === 'failed' ? 0 : 1
      const bf = b.target.status === 'failed' ? 0 : 1
      return af === bf ? a.index - b.index : af - bf
    })
    .map((item) => item.target)
}
