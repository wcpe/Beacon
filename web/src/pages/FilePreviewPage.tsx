// 文件树有效预览页（FR-45）：只读预览某服的有效文件树（逐文件合并结果 + 逐键/整文件来源）。
// 作为后续 FR-46 审核台 diff「期望合并值」一侧的数据源；不含写/编辑（写路径仍在通道B 既有端点）。

import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'

import { effectiveFiles, listInstances } from '../api/client'
import { Badge } from '@/components/ui/badge'
import FileEffectivePreview from './filepreview/FileEffectivePreview'

export default function FilePreviewPage() {
  // 预览目标（实例 serverId）；按 zone_assignment 解出大区/小区四层覆盖
  const [target, setTarget] = useState<{ serverId?: string; group?: string }>({})

  // 实例列表（目标下拉）
  const instancesQuery = useQuery({ queryKey: ['instances-all'], queryFn: () => listInstances({}) })

  // 有效文件树预览（仅在选定目标后拉取）
  const previewQuery = useQuery({
    queryKey: ['file-effective', target],
    queryFn: () =>
      effectiveFiles({
        namespace: 'prod',
        serverId: target.serverId,
        group: target.group,
      }),
    enabled: !!(target.serverId || target.group),
  })

  return (
    <div className="flex flex-col h-full overflow-hidden gap-2">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">文件树有效预览</h1>
        <Badge variant="outline" className="text-xs">
          只读 · 通道B 合并结果
        </Badge>
      </div>
      <div className="flex flex-1 min-h-0 rounded-lg border border-border bg-card overflow-hidden">
        <FileEffectivePreview
          instances={instancesQuery.data ?? []}
          target={target}
          onTargetChange={setTarget}
          isLoading={previewQuery.isLoading && !!(target.serverId || target.group)}
          data={previewQuery.data}
        />
      </div>
    </div>
  )
}
