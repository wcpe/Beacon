// 归档任务状态轮询判据（archive-block 列表 / 水位与 detail-panel 详情共用，避免重复定义）。
import type { ArchiveJob } from '@beacon/contracts'

// 非终态集合：存在此类任务时需轮询刷新进度，全部终态即停轮询（省请求）。
const ACTIVE_JOB_STATUSES = new Set<ArchiveJob['status']>(['pending', 'running', 'cancelling'])

// 列表 / 水位 / 详情轮询周期（毫秒）：归档由后台工作器异步推进，进行中任务需自动刷新才能看到进度。
export const ARCHIVE_POLL_MS = 5000

// isActiveArchiveStatus 判单个任务是否处于非终态（进行中）。
export function isActiveArchiveStatus(status: ArchiveJob['status']): boolean {
  return ACTIVE_JOB_STATUSES.has(status)
}

// hasActiveArchiveJob 判列表是否存在进行中任务（决定列表 / 水位是否继续轮询）。
export function hasActiveArchiveJob(items: ArchiveJob[] | undefined): boolean {
  return (items ?? []).some((job) => isActiveArchiveStatus(job.status))
}
