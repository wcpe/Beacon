// 生效预览：选择服务器/分组，展示该目标的有效配置（含来源链路与被删除的键）。

import { Badge } from '@/components/ui/badge'
import { ScrollArea } from '@/components/ui/scroll-area'
import type { EffectiveConfigView } from '../../api/client'
import type { InstanceView } from '../../api/types'

export default function EffectivePreview({
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
  data: EffectiveConfigView | null | undefined
}) {
  return (
    <div className="flex-1 flex flex-col min-h-0">
      {/* 生效预览目标选择 */}
      <div className="flex-shrink-0 flex items-center gap-2 px-3 py-1.5 border-b border-border bg-muted/20">
        <span className="text-xs text-muted-foreground">预览目标：</span>
        <select
          className="h-7 rounded border border-input bg-background px-2 text-xs"
          value={target.serverId ?? target.group ?? ''}
          onChange={(e) => {
            const val = e.target.value
            if (val.startsWith('server-')) {
              onTargetChange({ serverId: val })
            } else {
              onTargetChange({ group: val })
            }
          }}
        >
          <option value="">选择服务器/分组</option>
          {instances.map((inst) => (
            <option key={inst.serverId} value={inst.serverId}>
              {inst.serverId} ({inst.group}/{inst.zone})
            </option>
          ))}
        </select>
        {data && (
          <Badge variant="outline" className="text-xs">
            md5: {data.md5.slice(0, 8)}
          </Badge>
        )}
      </div>
      {/* 生效预览内容 */}
      {isLoading ? (
        <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">加载中…</div>
      ) : data ? (
        <ScrollArea className="flex-1">
          <div className="p-3 space-y-3">
            {data.items.map((item) => (
              <div key={item.dataId} className="rounded border border-border overflow-hidden">
                <div className="px-2 py-1 bg-muted/30 text-xs font-medium flex items-center justify-between">
                  <span>
                    {item.dataId} ({item.format})
                  </span>
                  <span className="text-muted-foreground font-mono">md5: {item.md5.slice(0, 8)}</span>
                </div>
                <pre className="p-2 text-xs font-mono whitespace-pre-wrap bg-background border-t border-border max-h-[200px] overflow-y-auto">
                  {item.content}
                </pre>
                {item.sources.length > 0 && (
                  <div className="px-2 py-1 bg-muted/10 border-t border-border">
                    <span className="text-[0.65rem] text-muted-foreground">来源：</span>
                    {item.sources.map((src, idx) => (
                      <span key={idx} className="ml-1 text-[0.65rem] text-blue-600">
                        {src.path.join('.')} ({src.scope})
                      </span>
                    ))}
                  </div>
                )}
              </div>
            ))}
            {data.deletions.length > 0 && (
              <div className="rounded border border-red-200 overflow-hidden">
                <div className="px-2 py-1 bg-red-50 text-xs font-medium text-red-600">
                  被删除的键（{data.deletions.length} 条）
                </div>
                {data.deletions.map((del, idx) => (
                  <div
                    key={idx}
                    className="px-2 py-0.5 text-xs text-red-500 font-mono border-t border-red-100"
                  >
                    {del.path.join('.')} ({del.scope})
                  </div>
                ))}
              </div>
            )}
          </div>
        </ScrollArea>
      ) : (
        <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
          选择服务器/分组查看生效配置
        </div>
      )}
    </div>
  )
}
