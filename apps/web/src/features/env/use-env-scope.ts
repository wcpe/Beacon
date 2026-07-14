// env 过滤器作用域解析（FR-178）：把顶栏选中的 env 解析为其映射的 namespace 集合，
// 供 namespace 作用域选择器（/zones /topology 共用的 NamespaceSelect）按 env 收窄可选范围。
// env 是纯展示 / 过滤维度：本 hook 只影响前端视图取数范围，不改任何权威数据。

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
 * - 返回 null 表示「全部环境」（不收窄，各页按原有全量作用域）；
 * - 返回数组表示选中 env 映射的 namespace id 列表（可能为空数组 = 该 env 未映射任何 namespace）。
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
