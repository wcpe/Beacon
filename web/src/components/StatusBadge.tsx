// 健康状态徽标：online 绿 / lost 琥珀 / offline 灰，基于 shadcn Badge。

import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import type { InstanceStatus } from '@/api/types'

// 状态到配色的映射（未知状态走默认 Badge 样式）
const COLOR_MAP: Record<InstanceStatus, string> = {
  online: 'bg-green-600 text-white',
  lost: 'bg-amber-500 text-white',
  offline: 'bg-muted text-muted-foreground',
}

export default function StatusBadge({ status }: { status: string }) {
  const color = COLOR_MAP[status as InstanceStatus]
  return <Badge className={cn('border-transparent', color)}>{status}</Badge>
}
