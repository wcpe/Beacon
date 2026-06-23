// 单个有效文件的合并结果卡片（FR-45 抽取）：展示合并后内容 + 逐键/整文件来源徽标 + 被删键标注。
// 由 FileEffectivePreview（有效树整列）与 FileFullPreview（全量预览中点开的追踪文件，FR-68）共用，避免重复。

import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import type { EffectiveFileItem } from '../../api/client'

export default function FileMergeCard({ file }: { file: EffectiveFileItem }) {
  const { t } = useTranslation()
  return (
    <div className="rounded border border-border overflow-hidden">
      <div className="px-2 py-1 bg-muted/30 text-xs font-medium flex items-center justify-between gap-2">
        <span className="font-mono break-all">{file.path}</span>
        <span className="flex items-center gap-1 shrink-0">
          {file.wholeFile ? (
            <Badge variant="secondary" className="text-[0.6rem]">
              {t('filePreview.wholeFile')}
            </Badge>
          ) : (
            <Badge variant="outline" className="text-[0.6rem]">
              {t('filePreview.deepMerge')}
            </Badge>
          )}
          <span className="text-muted-foreground font-mono">md5: {file.md5.slice(0, 8)}</span>
        </span>
      </div>
      <pre className="p-2 text-xs font-mono whitespace-pre-wrap bg-background border-t border-border max-h-[200px] overflow-y-auto">
        {file.content}
      </pre>
      {file.sources.length > 0 && (
        <div className="px-2 py-1 bg-muted/10 border-t border-border">
          <span className="text-[0.65rem] text-muted-foreground">
            {file.wholeFile ? t('filePreview.sourceWholeFile') : t('filePreview.sourceMerge')}
          </span>
          {file.sources.map((src, idx) => (
            <span key={idx} className="ml-1 text-[0.65rem] text-blue-600">
              {src.path.length > 0 ? `${src.path.join('.')} (${src.scope})` : src.scope}
            </span>
          ))}
        </div>
      )}
      {file.deletions.length > 0 && (
        <div className="bg-red-50/50 border-t border-red-100">
          <div className="px-2 py-1 text-[0.65rem] font-medium text-red-600">
            {t('filePreview.deletedTitle', { count: file.deletions.length })}
          </div>
          {file.deletions.map((del, idx) => (
            <div key={idx} className="px-2 py-0.5 text-[0.65rem] text-red-500 font-mono">
              {del.path.join('.')} ({del.scope})
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
