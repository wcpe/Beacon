// 热冷归档域响应契约（/admin/v2/archive/*）。
// 契约真源：docs/specs/v2-hot-cold-archive.md §5；任务状态机 §4.3。

import type { Paged } from './common'

export type ArchiveDomainName =
  | 'metric_sample'
  | 'health_snapshot'
  | 'sched_decision'
  | 'conn_detail'
  | 'msg_trace'
  | 'msg_payload'
  | 'audit'

export type ArchiveJobMode = 'dry_run' | 'execute'
export type ArchiveJobStatus = 'pending' | 'running' | 'succeeded' | 'failed' | 'cancelling' | 'cancelled'
export type ArchiveItemPhase = 'pending' | 'copying' | 'verifying' | 'deleting' | 'done' | 'failed' | 'skipped'

/** 归档总览的域行 */
export interface ArchiveDomainOverview {
  domain: ArchiveDomainName
  retentionDays: number
  hotRows: number
  archiveRows: number
  expiredRows: number
  lastJob: { id: number; mode: ArchiveJobMode; status: ArchiveJobStatus; finishedAt: string | null } | null
}

/** GET /admin/v2/archive/overview 响应 */
export interface ArchiveOverview {
  target: { mode: 'same-instance' | 'external'; database: string; dsnMasked: string; reachable: boolean }
  domains: ArchiveDomainOverview[]
}

/** 任务工作项（域 × 表 / 区间，断点续跑检查点） */
export interface ArchiveJobItem {
  id: number
  domain: ArchiveDomainName
  tableName: string
  rangeTo: string | null
  phase: ArchiveItemPhase
  cursor: string | null
  rowsExpected: number
  rowsCopied: number
  rowsDeleted: number
  verifyRowsHot: number | null
  verifyRowsArchive: number | null
  verifySampleSize: number | null
  verifyHashHot: string | null
  verifyHashArchive: string | null
  verifyPassed: boolean | null
  error: string | null
}

/** 归档任务 */
export interface ArchiveJob {
  id: number
  mode: ArchiveJobMode
  trigger: 'scheduled' | 'manual'
  status: ArchiveJobStatus
  domains: ArchiveDomainName[]
  operator: string
  error: string | null
  startedAt: string | null
  finishedAt: string | null
  createdAt: string
}

export interface ArchiveJobDetail extends ArchiveJob {
  items: ArchiveJobItem[]
}

export type ArchiveJobListResponse = Paged<ArchiveJob>
