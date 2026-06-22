// 文件树有效预览（FR-45）：某服视角列文件 → 看合并结果 + 逐键来源徽标 / 整文件来源 + 豁免与被删键标注。
// 只读展示组件（数据由页面传入），作为 FR-46 审核台 diff「期望合并值」一侧的数据源。
// 复用 FR-22 配置有效预览（EffectivePreview）的展示模式。

import { Badge } from '@/components/ui/badge'
import { ScrollArea } from '@/components/ui/scroll-area'
import type { EffectiveFileTreeView } from '../../api/client'
import type { InstanceView } from '../../api/types'

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
  return (
    <div className="flex-1 flex flex-col min-h-0">
      {/* 预览目标选择 */}
      <div className="flex-shrink-0 flex items-center gap-2 px-3 py-1.5 border-b border-border bg-muted/20">
        <span className="text-xs text-muted-foreground">预览目标：</span>
        <select
          className="h-7 rounded border border-input bg-background px-2 text-xs"
          value={target.serverId ?? target.group ?? ''}
          onChange={(e) => {
            const val = e.target.value
            if (!val) {
              onTargetChange({})
            } else {
              // 实例下拉一律以 serverId 作目标（按 zone_assignment 解出大区/小区）
              onTargetChange({ serverId: val })
            }
          }}
        >
          <option value="">选择服务器</option>
          {instances.map((inst) => (
            <option key={inst.serverId} value={inst.serverId}>
              {inst.serverId} ({inst.group}/{inst.zone})
            </option>
          ))}
        </select>
        {data && (
          <Badge variant="outline" className="text-xs">
            fileTreeMd5: {data.fileTreeMd5.slice(0, 8)}
          </Badge>
        )}
      </div>
      {/* 预览内容 */}
      {isLoading ? (
        <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">加载中…</div>
      ) : data ? (
        data.files.length === 0 ? (
          <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
            该目标无有效文件
          </div>
        ) : (
          <ScrollArea className="flex-1">
            <div className="p-3 space-y-3">
              {data.files.map((file) => (
                <div key={file.path} className="rounded border border-border overflow-hidden">
                  <div className="px-2 py-1 bg-muted/30 text-xs font-medium flex items-center justify-between gap-2">
                    <span className="font-mono break-all">{file.path}</span>
                    <span className="flex items-center gap-1 shrink-0">
                      {file.wholeFile ? (
                        <Badge variant="secondary" className="text-[0.6rem]">
                          整文件
                        </Badge>
                      ) : (
                        <Badge variant="outline" className="text-[0.6rem]">
                          深合并
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
                        {file.wholeFile ? '整文件来自：' : '来源：'}
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
                        被删除的键（{file.deletions.length} 条）
                      </div>
                      {file.deletions.map((del, idx) => (
                        <div key={idx} className="px-2 py-0.5 text-[0.65rem] text-red-500 font-mono">
                          {del.path.join('.')} ({del.scope})
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              ))}
            </div>
          </ScrollArea>
        )
      ) : (
        <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
          选择服务器查看有效文件树
        </div>
      )}
    </div>
  )
}
