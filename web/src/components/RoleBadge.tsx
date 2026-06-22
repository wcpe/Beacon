// 角色徽标：区分 BC 代理（bungee）与子服（bukkit）。
// bungee 紫、bukkit 蓝，与集群拓扑图（FR-37）的角色配色一致，便于跨页面对应。

import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

// 角色到配色的映射（未知角色走默认 Badge 样式并原样显示）；显示名经 i18n（role.<role>）。
const ROLE_CLASS: Record<string, string> = {
  bungee: 'bg-[#7c3aed] text-white',
  bukkit: 'bg-[#2563eb] text-white',
}

export default function RoleBadge({ role }: { role: string }) {
  const { t } = useTranslation()
  const className = ROLE_CLASS[role]
  return (
    <Badge className={cn('border-transparent', className)}>
      {t(`role.${role}`, { defaultValue: role })}
    </Badge>
  )
}
