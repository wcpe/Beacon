// 指标健康调度域数据获取（/dashboard /service-analysis）：走真实 /admin/v2/metrics*、/health*、
// /sched-decisions*（FR-146/147 已实现，形状对齐 @beacon/contracts）。读端点用于 useQuery；
// 错误按脱敏 message 抛出（ADR-0057）。

import type {
  HealthDetail,
  HealthItem,
  HealthSnapshotsResponse,
  MetricsSeriesResponse,
  MetricsSummary,
  Paged,
  SchedDecisionDetail,
  SchedDecisionItem,
  SchedDecisionSummary,
} from '@beacon/contracts'

import { buildQuery, request } from './http'

/** 集群聚合概览（dashboard 健康 / 调度总览） */
export function fetchMetricsSummary(): Promise<MetricsSummary> {
  return request('GET', '/admin/v2/metrics/summary')
}

export interface HealthListQuery {
  namespaceId?: number
  zone?: string
  level?: string
  schedulable?: boolean
  keyword?: string
  page?: number
  pageSize?: number
}

/** 集群健康列表（dashboard 服务器状态墙）：逐服健康分 / 等级 / 可调度。 */
export function fetchHealthList(query: HealthListQuery = {}): Promise<Paged<HealthItem>> {
  return request('GET', `/admin/v2/health${buildQuery({ ...query })}`)
}

export interface MetricsSeriesQuery {
  // 逗号分隔的 serverId（必填，禁止全量扫描）
  serverId: string
  // 聚合步长（秒）
  step?: number
  // 时间窗（RFC3339），缺省服务端按最近 1h
  from?: string
  to?: string
}

/** 单服 / 多服指标时序（service-analysis 多指标趋势与多服对比） */
export function fetchMetricsSeries(query: MetricsSeriesQuery): Promise<MetricsSeriesResponse> {
  return request('GET', `/admin/v2/metrics/series${buildQuery({ ...query })}`)
}

/** 单服健康详情（因子分解 + 权重版本） */
export function fetchHealthDetail(serverId: string): Promise<HealthDetail> {
  return request('GET', `/admin/v2/health/${serverId}`)
}

/** 调度决策概览（成功率 / 失败原因 Top / 降级占比） */
export function fetchSchedSummary(window: string): Promise<SchedDecisionSummary> {
  return request('GET', `/admin/v2/sched-decisions/summary${buildQuery({ window })}`)
}

export interface SchedDecisionsQuery {
  // 起止毫秒时间戳（后端必填，范围 ≤60 天）
  from: number
  to: number
  namespaceId?: number
  zone?: string
  // 匹配发起方或选中目标 serverId
  serverId?: string
  // 结果过滤：success / failed
  result?: string
  page?: number
  pageSize?: number
}

/** 调度决策记录分页查询（service-analysis 调度决策下钻） */
export function fetchSchedDecisions(query: SchedDecisionsQuery): Promise<Paged<SchedDecisionItem>> {
  return request('GET', `/admin/v2/sched-decisions${buildQuery({ ...query })}`)
}

/** 单条调度决策详情（含逐台排除原因，可解释「为什么没选某台」） */
export function fetchSchedDecisionDetail(traceId: string): Promise<SchedDecisionDetail> {
  return request('GET', `/admin/v2/sched-decisions/${traceId}`)
}

export interface HealthSnapshotsQuery {
  // 目标服务器（必填）
  serverId: string
  // 时间窗（RFC3339），缺省服务端按最近 1h
  from?: string
  to?: string
}

/** 健康快照回放（service-analysis 健康快照下钻：分数 / 等级随时间变化） */
export function fetchHealthSnapshots(query: HealthSnapshotsQuery): Promise<HealthSnapshotsResponse> {
  return request('GET', `/admin/v2/health/snapshots${buildQuery({ ...query })}`)
}
