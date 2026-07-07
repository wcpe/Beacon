// 指标健康调度域数据获取（/dashboard /service-analysis）：走 mock /admin/v2/metrics*、/health*、/sched-decisions*。
// 读端点用于 useQuery；错误按脱敏 message 抛出（ADR-0057）。

import type {
  HealthDetail,
  MetricsSeriesResponse,
  MetricsSummary,
  SchedDecisionSummary,
} from '@beacon/devmock'

import { buildQuery, request } from './http'

/** 集群聚合概览（dashboard 健康 / 调度总览） */
export function fetchMetricsSummary(): Promise<MetricsSummary> {
  return request('GET', '/admin/v2/metrics/summary')
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
