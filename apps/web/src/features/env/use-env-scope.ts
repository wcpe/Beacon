// env / 观测范围作用域解析（FR-178 / FR-213）：把页眉观测范围选择器选中的范围解析为其映射的 namespace 集合，
// 供各运维主路径页按范围收窄取数。范围是纯展示 / 过滤维度：只影响前端视图，不改权威数据。
//
// 语义：
// - null →「全部环境」，允许不带 namespace 参数请求；
// - 返回数组 → 仅可请求数组内的 namespace；空数组表示作用域无效或空映射，必须停止请求。
//
// ⚠️ 修复：作用域真源为页眉 observation-scope 选择器（FR-213 起页眉已切到它）。
// 此前误读 state/env-filter（该 store 自 FR-213 起再无写入方、恒为「全部」），
// 导致所有观测 / 集群页的 env 收窄静默失效；现统一以页眉实际选择为准。
import type { EnvItem } from '@beacon/contracts'
import { useQuery } from '@tanstack/react-query'

import { fetchEnvList } from '../../api/system'
import {
  ALL_OBSERVATION_ENV,
  deriveObservationScope,
  type DerivedObservationScope,
  useObservationScopeSelection,
} from './observation-scope'

/** 全量 env 选项查询（顶栏过滤器与作用域解析共用同一 query key，避免重复请求）。 */
export function useEnvOptionsQuery() {
  return useQuery({
    queryKey: ['envs', 'options'],
    queryFn: () => fetchEnvList({ pageSize: 100 }),
  })
}

/** 全量 env 选项（顶栏过滤器与作用域解析共用同一 query key，避免重复请求）。 */
export function useEnvOptions(): EnvItem[] {
  return useEnvOptionsQuery().data?.items ?? []
}

/**
 * 观测范围是否仍在解析（选了具体 env 但 env 选项尚未就绪）。
 *
 * 此间 `useEnvNamespaceScope()` 会返回空集合（fail-closed）——页面**应显示骨架而非空态**，
 * 否则会把「范围还在解析」误报成「无数据」。全部环境无需 env 选项即可解析，故不算 pending。
 */
export function useEnvScopePending(): boolean {
  const selection = useObservationScopeSelection()
  const query = useEnvOptionsQuery()
  return selection.kind === 'env' && query.isLoading
}

/** 将选中的 env id 解析为 namespace id 作用域；失效选项和空映射均停止查询。 */
export function resolveEnvNamespaceScope(
  envId: number,
  envs: readonly Pick<EnvItem, 'id' | 'namespaces'>[],
): number[] | null {
  if (envId === ALL_OBSERVATION_ENV) {
    return null
  }
  return envs.find((item) => item.id === envId)?.namespaces.map((namespace) => namespace.id) ?? []
}

/** 把页眉观测范围映射为受限 namespace id 集合（null=全部；[]=无效 / 空映射，必须停止请求）。 */
export function resolveObservationScopeNamespaceIds(
  scope: DerivedObservationScope,
  envs: readonly Pick<EnvItem, 'id' | 'namespaces'>[],
): number[] | null {
  if (scope.kind === 'invalid') {
    return []
  }
  // 页眉可进一步选具体 namespace（env 级或「全部环境」级）→ 收窄到该单 namespace。
  if (scope.namespaceId > 0) {
    return [scope.namespaceId]
  }
  if (scope.kind === 'all') {
    return null
  }
  return resolveEnvNamespaceScope(scope.envId, envs)
}

/** 当前页眉观测范围对应的 namespace id 集合（数据收窄真源）。 */
export function useEnvNamespaceScope(): number[] | null {
  const envs = useEnvOptions()
  return resolveObservationScopeNamespaceIds(deriveObservationScope(useObservationScopeSelection(), envs), envs)
}

/** 将选中的 env id 解析为 namespace 名称作用域；失效选项和空映射均停止查询。 */
export function resolveEnvNamespaceCodes(
  envId: number,
  envs: readonly Pick<EnvItem, 'id' | 'namespaces'>[],
): string[] | null {
  if (envId === ALL_OBSERVATION_ENV) {
    return null
  }
  return envs.find((item) => item.id === envId)?.namespaces.map((namespace) => namespace.name) ?? []
}

/** 当前页眉观测范围映射的 namespace 名称集合。 */
export function useEnvNamespaceCodes(): string[] | null {
  const envs = useEnvOptions()
  const ids = resolveObservationScopeNamespaceIds(deriveObservationScope(useObservationScopeSelection(), envs), envs)
  if (ids === null) {
    return null
  }
  const nameById = new Map(envs.flatMap((env) => env.namespaces).map((namespace) => [namespace.id, namespace.name]))
  return ids.map((id) => nameById.get(id)).filter((name): name is string => name !== undefined)
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
