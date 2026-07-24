// env 过滤器作用域解析（FR-178）：把顶栏选中的 env 解析为其映射的 namespace 集合，
// 供各运维主路径页按 env 收窄取数。env 是纯展示 / 过滤维度：只影响前端视图，不改权威数据。
//
// 语义：
// - useEnvNamespaceScope() === null →「全部环境」，不按 env 收窄
// - 返回 number[]（可为空）→ 仅这些 namespace id 可见

import type { EnvItem } from '@beacon/contracts'
import { useQuery } from '@tanstack/react-query'

import { fetchEnvList } from '../../api/system'
import { ALL_ENVS, useEnvFilter } from '../../state/env-filter'

/** 全量 env 选项（顶栏过滤器与作用域解析共用同一 query key，避免重复请求）。 */
export function useEnvOptions(): EnvItem[] {
  const query = useQuery({
    queryKey: ['envs', 'options'],
    queryFn: () => fetchEnvList({ pageSize: 100 }),
  })
  return query.data?.items ?? []
}

/**
 * 当前 env 过滤器对应的 namespace id 集合：
 * - 返回 null 表示「全部环境」（不收窄）；
 * - 返回数组表示选中 env 映射的 namespace id 列表（可能为空 = 该 env 未映射任何 namespace）。
 */
export function useEnvNamespaceScope(): number[] | null {
  const envId = useEnvFilter()
  const envs = useEnvOptions()
  if (envId === ALL_ENVS) {
    return null
  }
  const env = envs.find((item) => item.id === envId)
  // 选中的 env 尚未加载到（或已被删除）时按「不收窄」处理，避免瞬时把所有页面清空
  if (!env) {
    return null
  }
  return env.namespaces.map((ns) => ns.id)
}

/**
 * 当前 env 映射的 namespace 名称（code）集合；「全部环境」返回 null。
 * 供审计 / 命令等以 namespace 字符串过滤的端点使用。
 */
export function useEnvNamespaceCodes(): string[] | null {
  const envId = useEnvFilter()
  const envs = useEnvOptions()
  if (envId === ALL_ENVS) {
    return null
  }
  const env = envs.find((item) => item.id === envId)
  if (!env) {
    return null
  }
  return env.namespaces.map((ns) => ns.name)
}

/**
 * 按 env 作用域过滤带 namespaceId 的列表项。
 * scope === null 时原样返回；否则只保留 id 落在 scope 内的行。
 */
export function filterItemsByEnvScope<T extends { namespaceId: number }>(
  items: readonly T[],
  scope: number[] | null,
): T[] {
  if (scope === null) {
    return [...items]
  }
  if (scope.length === 0) {
    return []
  }
  const allowed = new Set(scope)
  return items.filter((item) => allowed.has(item.namespaceId))
}

/**
 * 按 env 作用域过滤带 namespace 字符串（code）的列表项。
 */
export function filterItemsByEnvCodes<T extends { namespace: string }>(
  items: readonly T[],
  codes: string[] | null,
): T[] {
  if (codes === null) {
    return [...items]
  }
  if (codes.length === 0) {
    return []
  }
  const allowed = new Set(codes)
  return items.filter((item) => allowed.has(item.namespace))
}

/**
 * 将页内 namespace 选择与 env 作用域合成 API 用的 namespaceId：
 * - selected === 0 或 null 且 env 为全部 → undefined（后端不传 = 全量）
 * - selected > 0 → 该 id
 * - env 收窄且仅 1 个 ns、未选手动选择 → 该唯一 id
 */
export function resolveApiNamespaceId(
  selected: number | null | undefined,
  envScope: number[] | null,
): number | undefined {
  if (selected !== null && selected !== undefined && selected > 0) {
    // 若 env 已收窄，选中的 ns 必须在范围内
    if (envScope !== null && !envScope.includes(selected)) {
      return envScope.length === 1 ? envScope[0] : undefined
    }
    return selected
  }
  if (envScope !== null && envScope.length === 1) {
    return envScope[0]
  }
  // 全部环境或 env 多 ns：不传 namespaceId，拉全量后客户端再滤（多 ns 时）
  return undefined
}

/** 是否需要在客户端按 envScope 二次过滤（env 映射多个 ns 时 API 单 id 不够）。 */
export function needsClientEnvFilter(envScope: number[] | null): boolean {
  return envScope !== null && envScope.length !== 1
}
