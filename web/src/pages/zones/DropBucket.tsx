// 容器：zone 桶与未指派池共用的视觉容器（标题 + 计数 + 卡片列表）。
// FR-71 移除 @dnd-kit useDroppable（取消拖拽归派），仅保留视觉布局。

import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

interface DropBucketProps {
  // 桶标题（如「未指派」或小区名）
  title: ReactNode
  // 右侧计数等附加信息
  meta?: ReactNode
  children: ReactNode
}

export default function DropBucket({ title, meta, children }: DropBucketProps) {
  return (
    <div
      className={cn(
        'flex min-h-24 flex-col gap-2 rounded-md border border-dashed border-border p-2',
      )}
    >
      <div className="flex items-center justify-between px-0.5">
        <span className="text-sm font-medium">{title}</span>
        {meta && <span className="text-xs text-muted-foreground">{meta}</span>}
      </div>
      <div className="flex flex-col gap-1.5">{children}</div>
    </div>
  )
}
