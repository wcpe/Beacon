// server 卡片：展示 serverId + 角色徽标（子服/BC）+ 在线状态点。
// FR-71 移除拖拽归派（@dnd-kit useDraggable），保留卡片视觉；解锁改派后由页面经 actions 注入逐卡操作按钮。

import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import type { InstanceView } from '../../api/types'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

// 状态 → 状态点配色（online 绿 / lost 琥珀 / offline 灰）
const DOT_COLOR: Record<string, string> = {
  online: 'bg-green-500',
  lost: 'bg-amber-500',
  offline: 'bg-muted-foreground',
}

// actions：解锁改派后由页面注入的逐卡操作（改派 / 取消指派）；未注入时仅展示卡片
export default function ServerCard({
  instance,
  actions,
}: {
  instance: InstanceView
  actions?: ReactNode
}) {
  const { t } = useTranslation()
  // 角色 → 中文徽标文案（bukkit=子服，bungee=BC；其他原样展示）
  const roleLabel = (role: string): string => {
    if (role === 'bukkit') return t('role.bukkit')
    if (role === 'bungee') return t('role.bungeeShort')
    return role
  }

  return (
    <div
      className={cn(
        'flex items-center gap-2 rounded-md border bg-card px-2.5 py-1.5 text-sm shadow-sm select-none',
      )}
    >
      <span
        aria-label={t('common.statusAria', { status: instance.status })}
        // FR-81：健康原因非空时悬浮显「Ns 未心跳 > 阈值 Ns」
        title={instance.healthReason || undefined}
        className={cn('size-2 shrink-0 rounded-full', DOT_COLOR[instance.status] ?? 'bg-muted-foreground')}
      />
      <span className="font-mono">{instance.serverId}</span>
      <Badge variant="secondary" className="ml-auto">
        {roleLabel(instance.role)}
      </Badge>
      {actions}
    </div>
  )
}
