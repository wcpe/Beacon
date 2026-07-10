// 指标健康调度域响应契约（/admin/v2/metrics*、health*、sched-decisions*、health-weights）。
// 契约真源：docs/specs/v2-metrics-health-scheduling.md §5.2；schedulable 原因码 §4.5、健康因子 §4.4。

export type HealthLevel = 'healthy' | 'degraded' | 'unhealthy'

/** 健康视图（列表项） */
export interface HealthItem {
  serverId: string
  namespaceId: number
  kind: string
  zoneName: string | null
  score: number
  level: HealthLevel
  schedulable: boolean
  reasons: string[]
  sampledAtMs: number
}

/** 健康因子分解 */
export interface HealthFactor {
  factor: string
  raw: number
  normalized: number
  weight: number
  applicable: boolean
}

/** 单服健康详情 */
export interface HealthDetail extends HealthItem {
  factors: HealthFactor[]
  weightsRev: number
}

/** 集群聚合概览 */
export interface MetricsSummary {
  generatedAt: string
  byKind: {
    proxy: { total: number; online: number }
    backend: { total: number; online: number }
  }
  playersOnline: number
  avgTps: number
  avgCpuPct: number
  levelDistribution: { healthy: number; degraded: number; unhealthy: number }
  schedulable: { yes: number; no: number }
}

/** 指标时序点（5s 批聚合行的服务端聚合） */
export interface MetricsSeriesPoint {
  tsMs: number
  cpuPctAvg: number
  cpuPctMax: number
  memUsedMbAvg: number
  tpsAvg: number
  tpsMin: number
  onlineAvg: number
  onlineMax: number
}

export interface MetricsSeriesResponse {
  stepSec: number
  series: { serverId: string; points: MetricsSeriesPoint[] }[]
}

/** 健康快照回放点 */
export interface HealthSnapshotPoint {
  tsMs: number
  score: number
  level: HealthLevel
  schedulable: boolean
  reasons: string[]
  weightsRev: number
}

/** 调度决策记录（列表项） */
export interface SchedDecisionItem {
  traceId: string
  tsMs: number
  namespaceId: number
  crossNamespace: boolean
  requesterServerId: string
  plugin: string | null
  purpose: string | null
  zoneName: string
  strategy: 'highest_score'
  source: 'control_plane' | 'local_fallback'
  weightsRev: number | null
  candidateCount: number
  excludedCount: number
  chosenServerId: string | null
  chosenScore: number
  failReason: string | null
  durationMs: number
}

/** 调度决策详情（含逐台排除原因） */
export interface SchedDecisionDetail extends SchedDecisionItem {
  excluded: { serverId: string; reason: string }[]
}

/** 决策概览 */
export interface SchedDecisionSummary {
  window: string
  total: number
  successCount: number
  successRatePercent: number
  failReasonTop: { reason: string; count: number }[]
  localFallbackPercent: number
}

/** 健康权重配置对象（§4.4） */
export interface HealthWeightsConfig {
  weights: Record<string, number>
  normalize: Record<string, number>
  levels: { healthyMin: number; degradedMin: number }
}

export interface HealthWeightsRev {
  rev: number
  config: HealthWeightsConfig
  operator: string
  createdAt: string
}

export interface HealthWeightsResponse {
  current: HealthWeightsRev
  history: HealthWeightsRev[]
}
