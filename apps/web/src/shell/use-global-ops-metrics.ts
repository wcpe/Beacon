// 全局运维指标（FR-188）：段 1 固定五项，复用现有 list/status API 前端聚合。
// 单项失败降级为 null（UI 显示「—」），15–30s 低频轮询，不整条崩溃。
import { useQueries } from '@tanstack/react-query'

import { fetchIdentities, fetchServers } from '../api/cluster'
import { fetchChangeOrders } from '../api/delivery-changes'
import { fetchAlertEvents } from '../api/observability'
import { fetchSystemStatus } from '../api/system'

/** 指标条轮询间隔（ms） */
export const GLOBAL_METRICS_REFETCH_MS = 20_000

/** 进行中变更单状态（活跃交付） */
const ACTIVE_CHANGE_STATUSES = new Set(['rolling', 'paused', 'pending_approval', 'approved', 'rolling_back'])

export interface GlobalOpsMetrics {
  /** 控制面是否在线（status 请求成功且库连通时为 true） */
  controlPlaneOnline: boolean | null
  /** Agent 在线实例数：优先 system.onlineInstances，否则 servers 列表 online 计数 */
  agentOnline: number | null
  /** 待确认注册数 */
  pendingRegistrations: number | null
  /** 未处理告警（open）数 */
  openAlerts: number | null
  /** 进行中变更单数 */
  activeChanges: number | null
}

function asCount(value: number | undefined): number | null {
  if (value === undefined || Number.isNaN(value)) {
    return null
  }
  return value
}

/**
 * 并行拉取五项全局态；任一 query 失败不影响其它项。
 * 返回的 metrics 中失败项为 null，供 UI 显示「—」。
 */
export function useGlobalOpsMetrics(): {
  metrics: GlobalOpsMetrics
  isFetching: boolean
} {
  const results = useQueries({
    queries: [
      {
        queryKey: ['shell', 'metrics', 'system-status'],
        queryFn: fetchSystemStatus,
        refetchInterval: GLOBAL_METRICS_REFETCH_MS,
        retry: 1,
      },
      {
        queryKey: ['shell', 'metrics', 'servers-online'],
        queryFn: () => fetchServers({ page: 1, pageSize: 100 }),
        refetchInterval: GLOBAL_METRICS_REFETCH_MS,
        retry: 1,
      },
      {
        queryKey: ['shell', 'metrics', 'pending-identities'],
        queryFn: () => fetchIdentities({ status: 'pending', page: 1, pageSize: 1 }),
        refetchInterval: GLOBAL_METRICS_REFETCH_MS,
        retry: 1,
      },
      {
        queryKey: ['shell', 'metrics', 'open-alerts'],
        queryFn: () => fetchAlertEvents({ page: 1, size: 100 }),
        refetchInterval: GLOBAL_METRICS_REFETCH_MS,
        retry: 1,
      },
      {
        queryKey: ['shell', 'metrics', 'active-changes'],
        queryFn: () => fetchChangeOrders({ page: 1, pageSize: 100 }),
        refetchInterval: GLOBAL_METRICS_REFETCH_MS,
        retry: 1,
      },
    ],
  })

  const [statusQ, serversQ, pendingQ, alertsQ, changesQ] = results

  let controlPlaneOnline: boolean | null = null
  if (statusQ.isSuccess) {
    controlPlaneOnline = statusQ.data.db.connected
  } else if (statusQ.isError) {
    controlPlaneOnline = false
  }

  let agentOnline: number | null = null
  if (statusQ.isSuccess) {
    agentOnline = asCount(statusQ.data.onlineInstances)
  } else if (serversQ.isSuccess) {
    agentOnline = serversQ.data.items.filter((s) => s.online).length
  } else if (serversQ.isError && statusQ.isError) {
    agentOnline = null
  }

  const pendingRegistrations = pendingQ.isSuccess
    ? asCount(pendingQ.data.total)
    : pendingQ.isError
      ? null
      : null

  let openAlerts: number | null = null
  if (alertsQ.isSuccess) {
    openAlerts = alertsQ.data.items.filter((a) => a.status === 'open').length
  } else if (alertsQ.isError) {
    openAlerts = null
  }

  let activeChanges: number | null = null
  if (changesQ.isSuccess) {
    activeChanges = changesQ.data.items.filter((c) => ACTIVE_CHANGE_STATUSES.has(c.status)).length
  } else if (changesQ.isError) {
    activeChanges = null
  }

  return {
    metrics: {
      controlPlaneOnline,
      agentOnline,
      pendingRegistrations,
      openAlerts,
      activeChanges,
    },
    isFetching: results.some((r) => r.isFetching),
  }
}
