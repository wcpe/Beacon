// env 过滤器作用域解析（FR-178）：把顶栏选中的 env 解析为其映射的 namespace 集合，
// 供各运维主路径页按 env 收窄取数。env 是纯展示 / 过滤维度：只影响前端视图，不改权威数据。
//
// 语义：
// - null →「全部环境」，允许不带 namespace 参数请求；
// - 返回数组 → 仅可请求数组内的 namespace；空数组表示作用域无效或空映射，必须停止请求。
//
import type { EnvItem } from '@beacon/contracts'
import { useQuery } from '@tanstack/react-query'

import { fetchEnvList } from '../../api/system'
import { ALL_ENVS, useEnvFilter } from '../../state/env-filter'
import { deriveObservationScope, useObservationScopeSelection } from './observation-scope'

/** 全量 env 选项（顶栏过滤器与作用域解析共用同一 query key，避免重复请求）。 */
export function useEnvOptions(): EnvItem[] {
  const query = useQuery({
    queryKey: ['envs', 'options'],
    queryFn: () => fetchEnvList({ pageSize: 100 }),
  })
  return query.data?.items ?? []
}

/** 将选中的 env id 解析为 namespace id 作用域；失效选项和空映射均停止查询。 */
export function resolveEnvNamespaceScope(
  envId: number,
  envs: readonly Pick<EnvItem, 'id' | 'namespaces'>[],
): number[] | null {
  if (envId === ALL_ENVS) {
    return null
  }
  return envs.find((item) => item.id === envId)?.namespaces.map((namespace) => namespace.id) ?? []
}

/** 当前 env 过滤器对应的 namespace id 集合。 */
export function useEnvNamespaceScope(): number[] | null {
  return resolveEnvNamespaceScope(useEnvFilter(), useEnvOptions())
}

/** 将选中的 env id 解析为 namespace 名称作用域；失效选项和空映射均停止查询。 */
export function resolveEnvNamespaceCodes(
  envId: number,
  envs: readonly Pick<EnvItem, 'id' | 'namespaces'>[],
): string[] | null {
  if (envId === ALL_ENVS) {
    return null
  }
  return envs.find((item) => item.id === envId)?.namespaces.map((namespace) => namespace.name) ?? []
}

/** 当前 env 映射的 namespace 名称集合。 */
export function useEnvNamespaceCodes(): string[] | null {
  return resolveEnvNamespaceCodes(useEnvFilter(), useEnvOptions())
}

/**
 * 页眉「观测范围」选择器（observation-scope 真源）解析出的 namespace 名称集合。
 * 与 useEnvNamespaceCodes 同语义（null=全部、[]=无效/空映射），但读的是**用户实际可切换**的页眉选择。
 * 供需要与 scope 端点（FR-213）保持同源的页面（如 /alert-events 批量写）使用。
 */
export function useObservationScopeNamespaceCodes(): string[] | null {
  const envs = useEnvOptions()
  const scope = deriveObservationScope(useObservationScopeSelection(), envs)
  if (scope.kind === 'all') {
    return null
  }
  if (scope.kind === 'invalid') {
    return []
  }
  return resolveEnvNamespaceCodes(scope.envId, envs)
}

/** 页眉观测范围对应的 scope 查询参数（envId/namespaceId），供 scope 感知端点带参（FR-213）。 */
export interface ObservationScopeQuery {
  envId?: number
  namespaceId?: number
}

/** 取当前页眉观测范围的 scope 查询参数；「全部环境」返回空对象（server 端即全量）。 */
export function useObservationScopeQuery(): ObservationScopeQuery {
  const envs = useEnvOptions()
  const scope = deriveObservationScope(useObservationScopeSelection(), envs)
  if (scope.kind === 'invalid') {
    // 无效范围：显式传一个必然越界的 namespaceId，令 scope 端点 fail-closed（不回退全量）。
    return { namespaceId: -1 }
  }
  if (scope.kind === 'all') {
    return scope.namespaceId > 0 ? { namespaceId: scope.namespaceId } : {}
  }
  return scope.namespaceId > 0 ? { envId: scope.envId, namespaceId: scope.namespaceId } : { envId: scope.envId }
}

/** 带分页元数据的受限请求结果。多 namespace 不提供跨 namespace 游标。 */
export interface EnvScopePage<T> {
  items: T[]
  total: number | null
  nextCursor?: string | null
}

interface EnvScopePageRequest {
  page?: number
  pageSize?: number
}

interface EnvScopePageOptions<T> extends EnvScopePageRequest {
  compare?: (left: T, right: T) => number
}

/** 按作用域聚合分页响应，保留全部环境/单 namespace 的游标；多 namespace 拉足前缀后归并裁剪。 */
export async function fetchPagedItemsByEnvScope<T, TNamespace extends string | number>(
  scope: readonly TNamespace[] | null,
  fetchPage: (namespace: TNamespace | undefined, pageRequest?: EnvScopePageRequest) => Promise<EnvScopePage<T>>,
  options: EnvScopePageOptions<T> = {},
): Promise<EnvScopePage<T>> {
  const page = options.page ?? 1
  const pageSize = options.pageSize
  const pageRequest = pageSize === undefined ? undefined : { page, pageSize }
  if (scope === null) {
    return pageRequest === undefined ? fetchPage(undefined) : fetchPage(undefined, pageRequest)
  }
  if (scope.length === 0) {
    return { items: [], total: 0, nextCursor: null }
  }
  if (scope.length === 1) {
    return pageRequest === undefined ? fetchPage(scope[0]) : fetchPage(scope[0], pageRequest)
  }
  const mergedRequest = pageSize === undefined ? undefined : { page: 1, pageSize: page * pageSize }
  const pages = await Promise.all(
    scope.map((namespace) =>
      mergedRequest === undefined ? fetchPage(namespace) : fetchPage(namespace, mergedRequest),
    ),
  )
  const items = pages.flatMap((current) => current.items)
  if (options.compare) {
    items.sort(options.compare)
  }
  return {
    items: pageSize === undefined ? items : items.slice((page - 1) * pageSize, page * pageSize),
    total: pages.every((current) => current.total !== null)
      ? pages.reduce((total, current) => total + (current.total ?? 0), 0)
      : null,
    nextCursor: null,
  }
}

/** 将页内 namespace 选择与 env 作用域合成受限请求范围。 */
export function resolveRequestNamespaceScope(
  selected: number | null | undefined,
  envScope: number[] | null,
): number[] | null {
  if (selected === null || selected === undefined || selected <= 0) {
    return envScope
  }
  if (envScope === null) {
    return [selected]
  }
  return envScope.includes(selected) ? [selected] : []
}

/**
 * 将页内 namespace 选择合成为单值 API 参数：
 * - undefined 仅表示全部环境；
 * - null 表示需要停止该单值请求（多 namespace、空映射或无效选择）；
 * - 数字表示可安全传给单值 API。
 */
export function resolveApiNamespaceId(
  selected: number | null | undefined,
  envScope: number[] | null,
): number | null | undefined {
  const scope = resolveRequestNamespaceScope(selected, envScope)
  if (scope === null) {
    return undefined
  }
  return scope.length === 1 ? scope[0] : null
}
