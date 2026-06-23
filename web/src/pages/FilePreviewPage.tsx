// 文件树预览页（FR-45 + FR-68）：两态切换——
// 「有效树预览」（FR-45，默认）：选服→看 Beacon 管的有效文件树（逐文件合并结果 + 逐键/整文件来源）；
// 「全量预览（含未追踪）」（FR-68）：选在线服→扫描全量磁盘清单与有效树交叉比对，列全量文件并区分追踪/未追踪。
// 不含写/编辑（写路径仍在通道B 既有端点 / 反向抓取）。

import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'

import { effectiveFiles, listInstances } from '../api/client'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import FileEffectivePreview from './filepreview/FileEffectivePreview'
import FileFullPreview from './filepreview/FileFullPreview'

// 预览模式：有效树（FR-45）/ 全量含未追踪（FR-68）
type PreviewMode = 'effective' | 'full'

export default function FilePreviewPage() {
  const { t } = useTranslation()
  // 当前预览模式
  const [mode, setMode] = useState<PreviewMode>('effective')
  // 有效树预览目标（实例 serverId）；按 zone_assignment 解出大区/小区四层覆盖
  const [target, setTarget] = useState<{ serverId?: string; group?: string }>({})

  // 实例列表（目标下拉 / 全量预览在线源）
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
        <h1 className="text-xl font-semibold">{t('filePreview.title')}</h1>
        <Badge variant="outline" className="text-xs">
          {t('filePreview.badge')}
        </Badge>
      </div>
      {/* 模式切换：有效树 / 全量含未追踪 */}
      <div className="flex items-center gap-1.5">
        <Button
          size="sm"
          variant={mode === 'effective' ? 'default' : 'outline'}
          onClick={() => setMode('effective')}
        >
          {t('filePreview.modeEffective')}
        </Button>
        <Button
          size="sm"
          variant={mode === 'full' ? 'default' : 'outline'}
          onClick={() => setMode('full')}
        >
          {t('filePreview.modeFull')}
        </Button>
      </div>
      <div className="flex flex-1 min-h-0 rounded-lg border border-border bg-card overflow-hidden">
        {mode === 'effective' ? (
          <FileEffectivePreview
            instances={instancesQuery.data ?? []}
            target={target}
            onTargetChange={setTarget}
            isLoading={previewQuery.isLoading && !!(target.serverId || target.group)}
            data={previewQuery.data}
          />
        ) : (
          <FileFullPreview instances={instancesQuery.data ?? []} />
        )}
      </div>
    </div>
  )
}
