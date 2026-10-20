// 交付域命名空间范围读取：只读取页眉观测范围，不猜默认值，也不改写用户的全局选择。
// 未选具体命名空间时，页面交由调用方显示引导并禁用依赖明确目标的写操作。
import { deriveObservationScope, useObservationScopeSelection } from './observation-scope'
import { useEnvOptionsQuery } from './use-env-scope'

/** 观测命名空间解析结果：页面据此区分加载中、加载失败与尚未选定。 */
export interface ObservationNamespaceState {
  /** 当前生效的命名空间 id；0 表示尚未选择具体命名空间或范围无效。 */
  namespaceId: number
  /** 环境选项仍在加载；此时应给加载态而非引导。 */
  pending: boolean
  /** 环境选项首次加载失败时保留错误，供页面展示并提供重试入口。 */
  error: Error | null
  /** 重新加载环境与命名空间选项。 */
  retry: () => void
}

/**
 * 读取当前命名空间范围，并区分选项加载、加载失败与尚未选定三种状态。
 * 未选具体命名空间时保留页眉原范围；页面不得从环境选项中自动挑选写入目标。
 */
export function useObservationNamespaceState(): ObservationNamespaceState {
  const selection = useObservationScopeSelection()
  const query = useEnvOptionsQuery()
  const envs = query.data?.items ?? []
  const error = query.isError ? query.error : null
  const namespaceId = error === null ? deriveObservationScope(selection, envs).namespaceId : 0
  const pending = error === null && selection.kind !== 'invalid' && query.isLoading

  return {
    namespaceId,
    pending,
    error,
    retry: () => {
      void query.refetch()
    },
  }
}
