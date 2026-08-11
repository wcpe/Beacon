// 首次分配写闭环的共享 hook：拖拽落区与勾选批量分配复用同一 mutation，
// 统一处理成功失效缓存、逐台结果与脱敏错误，避免两处重复 mutation 逻辑。
import { useMutation, useQueryClient } from '@tanstack/react-query'

import { ApiClientError, type ApprovalTicket, assignServers } from '../../api/cluster'

// 分配目标：小区（落 backend）或 BC 集群（落 proxy）
export interface AssignTarget {
  kind: 'zone' | 'bc_cluster'
  id: number
}

export interface AssignVars {
  serverIds: number[]
  target: AssignTarget
  isDefaultEntry?: boolean
  reason: string
}

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

/** 首次分配仅创建审批申请；成功后刷新读取缓存，实际归属由 worker 执行。 */
export function useAssignServers(onSettled?: () => void) {
  const queryClient = useQueryClient()

  return useMutation<ApprovalTicket, unknown, AssignVars>({
    mutationFn: ({ serverIds, target, isDefaultEntry, reason }) =>
      assignServers({ serverIds, target, isDefaultEntry, reason }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['servers'] })
      await queryClient.invalidateQueries({ queryKey: ['zone-tree'] })
      onSettled?.()
    },
  })
}

export { messageOf }
