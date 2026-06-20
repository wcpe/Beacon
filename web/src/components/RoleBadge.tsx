// 角色徽标：区分 BC 代理（bungee）与子服（bukkit）。
// bungee 紫、bukkit 蓝，与集群拓扑图（FR-37）的角色配色一致，便于跨页面对应。

import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

// 角色到「显示名 + 配色」的映射（未知角色走默认 Badge 样式并原样显示）。
const ROLE_MAP: Record<string, { label: string; className: string }> = {
  bungee: { label: 'BC 代理', className: 'bg-[#7c3aed] text-white' },
  bukkit: { label: '子服', className: 'bg-[#2563eb] text-white' },
}

export default function RoleBadge({ role }: { role: string }) {
  const conf = ROLE_MAP[role]
  return (
    <Badge className={cn('border-transparent', conf?.className)}>
      {conf ? conf.label : role}
    </Badge>
  )
}
