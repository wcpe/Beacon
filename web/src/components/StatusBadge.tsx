// 健康状态徽标：online 绿 / lost 琥珀 / offline 灰，基于 shadcn Badge。

import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import type { InstanceStatus } from '@/api/types'

// 状态到配色的映射（未知状态走默认 Badge 样式）
const COLOR_MAP: Record<InstanceStatus, string> = {
  online: 'bg-green-600 text-white',
  lost: 'bg-amber-500 text-white',
  offline: 'bg-muted text-muted-foreground',
}

// reason 为触发当前状态的原因文案（FR-81）：非空时以原生 title 悬浮提示展示（如「35s 未心跳 > ttl 30s」）
export default function StatusBadge({ status, reason }: { status: string; reason?: string }) {
  const { t } = useTranslation()
  const color = COLOR_MAP[status as InstanceStatus]
  // 状态 label 经 i18n 映射（zh-CN 保留英文原值，未知状态回退原值），渲染文本不变
  // reason 空串 / 未传时不设 title（online 无原因），避免空悬浮框
  return (
    <Badge className={cn('border-transparent', color)} title={reason || undefined}>
      {t(`status.${status}`, { defaultValue: status })}
    </Badge>
  )
}
