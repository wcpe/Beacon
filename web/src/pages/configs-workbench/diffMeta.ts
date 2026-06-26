// 同步状态 / 服务器标记 → 状态图标(lucide) / 语义色 / i18n 文案键的统一映射（FR-111 图例 + 行内状态同源）。
// 改进 2：受管侧行首由纯色圆点改为 lucide 状态图标（✓已同步 / ⟳同步中 / ✗未同步有差异 / ↑仅受管未下发 / ✗服务器已删）。
// 色用语义类（emerald/amber/sky/destructive/muted），暗色自动适配。

import type { ComponentType } from 'react'
import {
  AlertTriangle,
  CircleCheck,
  CircleX,
  CloudUpload,
  Loader2,
  type LucideProps,
} from 'lucide-react'
import type { ServerMark, SyncStatus } from '@/api/mock/workbench'

export interface DotMeta {
  // 行首状态图标（lucide 组件）
  icon: ComponentType<LucideProps>
  // 图标语义色类（前景色）
  iconClass: string
  // 是否旋转（同步中态）
  spin?: boolean
  // 图例 / tooltip 文案 i18n 键
  labelKey: string
}

// 受管侧同步状态四态（改进 2，色球→图标）：
// 已同步一致(✓绿) / 有差异待下发(✗琥珀) / 仅受管未下发(↑蓝) / 服务器已删(✗红)
export const SYNC_META: Record<SyncStatus, DotMeta> = {
  synced: { icon: CircleCheck, iconClass: 'text-emerald-500', labelKey: 'configs.workbench.syncSynced' },
  drift: { icon: CircleX, iconClass: 'text-amber-500', labelKey: 'configs.workbench.syncDrift' },
  'managed-only': { icon: CloudUpload, iconClass: 'text-sky-500', labelKey: 'configs.workbench.syncManagedOnly' },
  'server-gone': { icon: CircleX, iconClass: 'text-destructive', labelKey: 'configs.workbench.syncServerGone' },
}

// 同步中态（改进 2）：发布 / 下发进行中的瞬态，图例单列展示（数据态由队列承载，不在树固定态里）
export const SYNCING_META: DotMeta = {
  icon: Loader2,
  iconClass: 'text-sky-500',
  spin: true,
  labelKey: 'configs.workbench.syncSyncing',
}

// 受管侧图例顺序（含同步中瞬态，仅图例展示）
export const SYNC_LEGEND_META: DotMeta[] = [
  SYNC_META.synced,
  SYNCING_META,
  SYNC_META.drift,
  SYNC_META['managed-only'],
  SYNC_META['server-gone'],
]

// 服务器侧纳管标记（右面板行首图标）：已纳管一致(✓绿) / 有差异(⚠琥珀) / 未纳管(○灰)
export const SERVER_MARK_META: Record<ServerMark, DotMeta> = {
  tracked: { icon: CircleCheck, iconClass: 'text-emerald-500', labelKey: 'configs.workbench.markTracked' },
  drift: { icon: AlertTriangle, iconClass: 'text-amber-500', labelKey: 'configs.workbench.markDrift' },
  untracked: { icon: CircleX, iconClass: 'text-muted-foreground/50', labelKey: 'configs.workbench.markUntracked' },
}

// 覆盖层 → badge 着色语义（全局蓝 / 组琥珀 / 实例灰）+ 文案键
export const SCOPE_META: Record<string, { badgeClass: string; labelKey: string }> = {
  global: { badgeClass: 'border-sky-500/40 text-sky-600 dark:text-sky-400', labelKey: 'configs.workbench.scopeGlobal' },
  group: { badgeClass: 'border-amber-500/40 text-amber-600 dark:text-amber-400', labelKey: 'configs.workbench.scopeGroup' },
  server: { badgeClass: 'border-muted-foreground/40 text-muted-foreground', labelKey: 'configs.workbench.scopeServer' },
}
