// 首次分配写闭环的共享 hook：拖拽落区与勾选批量分配复用同一 mutation，
// 统一处理成功失效缓存、逐台结果与脱敏错误，避免两处重复 mutation 逻辑。
import { useMutation, useQueryClient } from '@tanstack/react-query'

import type { AssignmentResult } from '@beacon/contracts'

import { ApiClientError, assignServers } from '../../api/cluster'

// 分配目标：小区（落 backend）或 BC 集群（落 proxy）
export interface AssignTarget {
  kind: 'zone' | 'bc_cluster'
  id: number
}

export interface AssignVars {
  serverIds: number[]
  target: AssignTarget
  isDefaultEntry?: boolean
}

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

/** 首次分配 mutation 封装：成功后失效 servers / zone-tree，暴露逐台结果与错误文案。 */
export function useAssignServers(onSettled?: () => void) {
  const queryClient = useQueryClient()

  return useMutation<{ results: AssignmentResult[] }, unknown, AssignVars>({
    mutationFn: ({ serverIds, target, isDefaultEntry }) =>
      assignServers({ serverIds, target, isDefaultEntry }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['servers'] })
      await queryClient.invalidateQueries({ queryKey: ['zone-tree'] })
      onSettled?.()
    },
  })
}

export { messageOf }
