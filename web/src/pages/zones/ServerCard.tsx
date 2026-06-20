// 可拖拽的 server 卡片：展示 serverId + 角色徽标（子服/BC）+ 在线状态点。
// 拖动由 @dnd-kit useDraggable 驱动；卡片 id = serverId（onDragEnd 据此查实例）。

import { useDraggable } from '@dnd-kit/core'
import { CSS } from '@dnd-kit/utilities'
import type { InstanceView } from '../../api/types'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

// 角色 → 中文徽标文案（bukkit=子服，bungee=BC 代理；其他原样展示）
function roleLabel(role: string): string {
  if (role === 'bukkit') return '子服'
  if (role === 'bungee') return 'BC'
  return role
}

// 状态 → 状态点配色（online 绿 / lost 琥珀 / offline 灰）
const DOT_COLOR: Record<string, string> = {
  online: 'bg-green-500',
  lost: 'bg-amber-500',
  offline: 'bg-muted-foreground',
}

export default function ServerCard({ instance }: { instance: InstanceView }) {
  // 用 serverId 作为拖拽 id；拖动时透传实例供叠层预览（DragOverlay 复用同组件）
  const { attributes, listeners, setNodeRef, transform, isDragging } = useDraggable({
    id: instance.serverId,
    data: { instance },
  })

  return (
    <div
      ref={setNodeRef}
      style={{ transform: CSS.Translate.toString(transform) }}
      {...listeners}
      {...attributes}
      // 在线状态点用 aria-label 暴露，便于无障碍与测试断言
      className={cn(
        'flex cursor-grab items-center gap-2 rounded-md border bg-card px-2.5 py-1.5 text-sm shadow-sm select-none',
        'active:cursor-grabbing',
        isDragging && 'opacity-50',
      )}
    >
      <span
        aria-label={`状态 ${instance.status}`}
        className={cn('size-2 shrink-0 rounded-full', DOT_COLOR[instance.status] ?? 'bg-muted-foreground')}
      />
      <span className="font-mono">{instance.serverId}</span>
      <Badge variant="secondary" className="ml-auto">
        {roleLabel(instance.role)}
      </Badge>
    </div>
  )
}
