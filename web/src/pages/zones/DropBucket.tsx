// 放置桶：zone 容器与未指派池共用。拖卡悬停时高亮，作为 @dnd-kit 的 droppable 区域。

import type { ReactNode } from 'react'
import { useDroppable } from '@dnd-kit/core'
import { cn } from '@/lib/utils'

interface DropBucketProps {
  // droppable id：未指派池为固定值，zone 桶为 encodeZoneDroppableId 编码值
  id: string
  // 桶标题（如「未指派」或小区名）
  title: ReactNode
  // 右侧计数等附加信息
  meta?: ReactNode
  children: ReactNode
}

export default function DropBucket({ id, title, meta, children }: DropBucketProps) {
  const { setNodeRef, isOver } = useDroppable({ id })

  return (
    <div
      ref={setNodeRef}
      className={cn(
        'flex min-h-24 flex-col gap-2 rounded-md border border-dashed p-2 transition-colors',
        isOver ? 'border-primary bg-primary/5' : 'border-border',
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
