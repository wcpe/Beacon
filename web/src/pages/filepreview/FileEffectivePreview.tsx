// 文件树有效预览（FR-45）：某服视角列文件 → 看合并结果 + 逐键来源徽标 / 整文件来源 + 豁免与被删键标注。
// 只读展示组件（数据由页面传入），作为 FR-46 审核台 diff「期望合并值」一侧的数据源。
// 复用 FR-22 配置有效预览（EffectivePreview）的展示模式。

import { useTranslation } from 'react-i18next'
import { Badge } from '@beacon/ui'
import { ScrollArea } from '@beacon/ui'
import type { EffectiveFileTreeView } from '../../api/client'
import type { InstanceView } from '../../api/types'
import FileMergeCard from './FileMergeCard'
import { Combobox } from '@beacon/ui'

export default function FileEffectivePreview({
  instances,
  target,
  onTargetChange,
  isLoading,
  data,
}: {
  instances: InstanceView[]
  target: { serverId?: string; group?: string }
  onTargetChange: (t: { serverId?: string; group?: string }) => void
  isLoading: boolean
  data: EffectiveFileTreeView | null | undefined
}) {
  const { t } = useTranslation()
  return (
    <div className="flex-1 flex flex-col min-h-0">
      {/* 预览目标选择 */}
      <div className="flex-shrink-0 flex items-center gap-2 px-3 py-1.5 border-b border-border bg-muted/20">
        <span className="text-xs text-muted-foreground">{t('filePreview.targetLabel')}</span>
        <Combobox
          className="w-72"
          value={target.serverId ?? target.group ?? ''}
          onChange={(val) => {
            if (!val) {
              onTargetChange({})
            } else {
              // 实例下拉一律以 serverId 作目标（按 zone_assignment 解出大区/小区）
              onTargetChange({ serverId: val })
            }
          }}
          options={instances.map((inst) => ({
            value: inst.serverId,
            label: `${inst.serverId} (${inst.group}/${inst.zone ?? '-'})`,
          }))}
          allowCustom={false}
          placeholder={t('filePreview.selectPlaceholder')}
        />
        {data && (
          <Badge variant="outline" className="text-xs">
            fileTreeMd5: {data.fileTreeMd5.slice(0, 8)}
          </Badge>
        )}
      </div>
      {/* 预览内容 */}
      {isLoading ? (
        <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
          {t('filePreview.loading')}
        </div>
      ) : data ? (
        data.files.length === 0 ? (
          <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
            {t('filePreview.noFiles')}
          </div>
        ) : (
          <ScrollArea className="flex-1">
            <div className="p-3 space-y-3">
              {data.files.map((file) => (
                <FileMergeCard key={file.path} file={file} />
              ))}
            </div>
          </ScrollArea>
        )
      ) : (
        <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
          {t('filePreview.empty')}
        </div>
      )}
    </div>
  )
}
