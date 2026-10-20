// 页眉控制面资源快照：独立于全局运维指标条，以低频轮询避免额外请求风暴。
import { useQuery } from '@tanstack/react-query'

import { fetchSystemStatus } from '../api/system'

/** 控制面资源指标轮询间隔（ms）。 */
export const CONTROL_PLANE_RESOURCES_REFETCH_MS = 8_000

export function useControlPlaneResources() {
  const query = useQuery({
    queryKey: ['shell', 'control-plane-resources'],
    queryFn: fetchSystemStatus,
    refetchInterval: CONTROL_PLANE_RESOURCES_REFETCH_MS,
    retry: 1,
  })

  // React Query 在轮询失败时保留上次成功的数据；首个请求失败则由展示组件显示占位符。
  return { data: query.data ?? null, isFetching: query.isFetching }
}
