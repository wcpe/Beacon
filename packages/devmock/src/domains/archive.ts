// 热冷归档域 mock（/admin/v2/archive/*）。
// 契约真源：docs/specs/v2-hot-cold-archive.md §5；任务状态机 §4.3（单飞约束、断点续跑、取消）。

import { HttpResponse, type HttpHandler } from 'msw'
import type {
  ArchiveDomainName,
  ArchiveJob,
  ArchiveJobDetail,
  ArchiveJobItem,
  ArchiveJobListResponse,
  ArchiveJobMode,
  ArchiveJobStatus,
  ArchiveOverview,
} from '@beacon/contracts'
import { jsonError, mockGet, mockPost, paginate, pathParam, queryStr, readBody } from '../http'
import type { MockScenario } from '../scenario'
import { defineScenarioStore } from '../store'
import { createRng, isoOffset, pseudoSha256 } from '../support'

const ALL_DOMAINS: readonly ArchiveDomainName[] = [
  'metric_sample',
  'health_snapshot',
  'sched_decision',
  'conn_detail',
  'msg_trace',
  'msg_payload',
  'audit',
]

const RETENTION: Record<ArchiveDomainName, number> = {
  metric_sample: 14,
  health_snapshot: 30,
  sched_decision: 60,
  conn_detail: 60,
  msg_trace: 60,
  msg_payload: 30,
  audit: 180,
}

interface ArchiveState {
  jobs: ArchiveJobDetail[]
  nextId: number
  reachable: boolean
}

const DAY = 86_400_000

function makeItems(jobId: number, domains: readonly ArchiveDomainName[], mode: ArchiveJobMode, done: boolean, failAt?: number): ArchiveJobItem[] {
  const rng = createRng(jobId * 7)
  return domains.map((domain, index) => {
    const rows = 10_000 + Math.floor(rng() * 90_000)
    const failed = failAt !== undefined && index === failAt
    const finished = done && !failed
    const hash = pseudoSha256(`verify:${String(jobId)}:${domain}`)
    return {
      id: jobId * 100 + index,
      domain,
      tableName: domain === 'audit' ? 'audit_log' : `${domain}_20260601`,
      rangeTo: domain === 'audit' ? isoOffset(-RETENTION[domain] * DAY) : null,
      phase: failed ? 'failed' : finished ? 'done' : mode === 'dry_run' ? 'done' : 'copying',
      cursor: finished ? String(rows) : failed ? String(Math.floor(rows / 2)) : null,
      rowsExpected: rows,
      rowsCopied: finished ? rows : failed ? Math.floor(rows / 2) : 0,
      rowsDeleted: finished && mode === 'execute' ? rows : 0,
      verifyRowsHot: finished ? rows : null,
      verifyRowsArchive: finished ? rows : null,
      verifySampleSize: finished ? 100 : null,
      verifyHashHot: finished ? hash : null,
      verifyHashArchive: finished ? hash : null,
      verifyPassed: finished ? true : failed ? false : null,
      error: failed ? '归档库写入失败：目标表锁等待超时（已脱敏）' : null,
    }
  })
}

function buildArchive(scenario: MockScenario): ArchiveState {
  if (scenario === 'empty') {
    return { jobs: [], nextId: 1, reachable: true }
  }
  const jobCount = scenario === 'huge' ? 60 : 4
  const jobs: ArchiveJobDetail[] = []
  for (let i = 0; i < jobCount; i++) {
    const id = i + 1
    const failed = i % 4 === 2
    const mode: ArchiveJobMode = i % 4 === 1 ? 'dry_run' : 'execute'
    const status: ArchiveJobStatus = failed ? 'failed' : i % 4 === 3 ? 'cancelled' : 'succeeded'
    jobs.push({
      id,
      mode,
      trigger: i % 2 === 0 ? 'scheduled' : 'manual',
      status,
      domains: [...ALL_DOMAINS],
      operator: i % 2 === 0 ? 'system' : 'ops-chen',
      error: failed ? '归档任务失败：msg_trace 分片校验不一致，热库数据未删除' : null,
      startedAt: isoOffset(-(jobCount - i) * DAY),
      finishedAt: status === 'succeeded' || status === 'failed' ? isoOffset(-(jobCount - i) * DAY + 3_600_000) : null,
      createdAt: isoOffset(-(jobCount - i) * DAY),
      items: makeItems(id, ALL_DOMAINS, mode, status === 'succeeded', failed ? 4 : undefined),
    })
  }
  jobs.reverse()
  return { jobs, nextId: jobCount + 1, reachable: true }
}

const getArchiveState: () => ArchiveState = defineScenarioStore(buildArchive)

function toJob(detail: ArchiveJobDetail): ArchiveJob {
  const job: ArchiveJob & { items?: unknown } = { ...detail }
  delete job.items
  return job
}

export const archiveHandlers: HttpHandler[] = [
  // 归档总览：目标库、各域水位与保留期、最近任务
  mockGet('/admin/v2/archive/overview', () => {
    const state = getArchiveState()
    const rng = createRng(6501)
    const overview: ArchiveOverview = {
      target: {
        mode: 'same-instance',
        database: 'beacon_archive',
        dsnMasked: 'mysql://***:***@10.30.0.5:3306/beacon_archive',
        reachable: state.reachable,
      },
      domains: ALL_DOMAINS.map((domain) => {
        const lastJob = state.jobs.find((job) => job.domains.includes(domain)) ?? null
        return {
          domain,
          retentionDays: RETENTION[domain],
          hotRows: state.jobs.length === 0 ? 0 : 100_000 + Math.floor(rng() * 4_000_000),
          archiveRows: state.jobs.length === 0 ? 0 : Math.floor(rng() * 60_000_000),
          expiredRows: state.jobs.length === 0 ? 0 : Math.floor(rng() * 800_000),
          lastJob: lastJob ? { id: lastJob.id, mode: lastJob.mode, status: lastJob.status, finishedAt: lastJob.finishedAt } : null,
        }
      }),
    }
    return HttpResponse.json(overview)
  }),

  // 任务列表（status / mode / trigger 过滤 + 分页）
  mockGet('/admin/v2/archive/jobs', ({ request }) => {
    const url = new URL(request.url)
    const status = queryStr(url, 'status')
    const mode = queryStr(url, 'mode')
    const trigger = queryStr(url, 'trigger')
    const rows = getArchiveState()
      .jobs.filter((job) => {
        if (status !== null && job.status !== status) {
          return false
        }
        if (mode !== null && job.mode !== mode) {
          return false
        }
        if (trigger !== null && job.trigger !== trigger) {
          return false
        }
        return true
      })
      .map(toJob)
    const { items, total } = paginate(rows, url)
    return HttpResponse.json({ items, total } satisfies ArchiveJobListResponse)
  }),

  // 创建任务（dry-run / 执行）；已有 running 任务 → 409（单飞约束）
  mockPost('/admin/v2/archive/jobs', async ({ request }) => {
    const body = await readBody<{ mode?: ArchiveJobMode; domains?: ArchiveDomainName[] }>(request)
    if (body.mode !== 'dry_run' && body.mode !== 'execute') {
      return jsonError(400, 'invalid_param', 'mode 必须为 dry_run 或 execute')
    }
    const state = getArchiveState()
    if (state.jobs.some((job) => job.status === 'running' || job.status === 'pending')) {
      return jsonError(409, 'archive_job_running', '已有归档任务进行中（单飞约束）')
    }
    const domains = Array.isArray(body.domains) && body.domains.length > 0 ? body.domains : [...ALL_DOMAINS]
    const id = state.nextId
    state.nextId += 1
    const dryRun = body.mode === 'dry_run'
    const job: ArchiveJobDetail = {
      id,
      mode: body.mode,
      trigger: 'manual',
      status: dryRun ? 'succeeded' : 'running',
      domains,
      operator: 'admin',
      error: null,
      startedAt: isoOffset(0),
      finishedAt: dryRun ? isoOffset(0) : null,
      createdAt: isoOffset(0),
      items: makeItems(id, domains, body.mode, dryRun),
    }
    state.jobs.unshift(job)
    return HttpResponse.json(job, { status: 201 })
  }),

  // 任务详情（逐 item 进度与校验结果）
  mockGet('/admin/v2/archive/jobs/:id', (info) => {
    const id = Number.parseInt(pathParam(info, 'id'), 10)
    const job = getArchiveState().jobs.find((j) => j.id === id)
    if (!job) {
      return jsonError(404, 'archive_job_not_found', '归档任务不存在')
    }
    return HttpResponse.json(job)
  }),

  // 失败任务断点续跑（仅 failed 可重试）
  mockPost('/admin/v2/archive/jobs/:id/retry', (info) => {
    const id = Number.parseInt(pathParam(info, 'id'), 10)
    const job = getArchiveState().jobs.find((j) => j.id === id)
    if (!job) {
      return jsonError(404, 'archive_job_not_found', '归档任务不存在')
    }
    if (job.status !== 'failed') {
      return jsonError(409, 'illegal_state', '仅 failed 任务可重试')
    }
    job.status = 'running'
    job.error = null
    for (const item of job.items) {
      if (item.phase === 'failed') {
        item.phase = 'copying'
        item.error = null
      }
    }
    return HttpResponse.json(job)
  }),

  // 取消任务（仅 pending / running 可取消）
  mockPost('/admin/v2/archive/jobs/:id/cancel', (info) => {
    const id = Number.parseInt(pathParam(info, 'id'), 10)
    const job = getArchiveState().jobs.find((j) => j.id === id)
    if (!job) {
      return jsonError(404, 'archive_job_not_found', '归档任务不存在')
    }
    if (job.status !== 'pending' && job.status !== 'running') {
      return jsonError(409, 'illegal_state', '仅 pending / running 任务可取消')
    }
    job.status = 'cancelled'
    job.finishedAt = isoOffset(0)
    return HttpResponse.json(job)
  }),
]
