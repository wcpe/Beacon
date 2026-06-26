// 侧栏导航单一真源（FR-93，方案 A 分组常驻 + 图标）：把导航分组树与扁平叶子集中到一处，
// Layout 按分组树形渲染（分区标题常驻 + 叶子常驻显示）、CommandPalette 取扁平叶子做导航目标——消除两份重复清单的漂移。
// 纯数据 + 派生，无副作用、可单测。

import type { LucideIcon } from 'lucide-react'
import {
  LayoutDashboard,
  SlidersHorizontal,
  FolderTree,
  Stamp,
  DownloadCloud,
  Server,
  Network,
  MapPin,
  ChartLine,
  ScrollText,
  Bell,
  Settings,
  RefreshCw,
  Activity,
  KeyRound,
  Layers,
  Terminal,
} from 'lucide-react'

// 单个导航叶子：路由 + i18n 文案键 + 语义图标。
export interface NavLeaf {
  to: string
  labelKey: string
  // 叶子前缀图标（lucide-react），由 Layout 以 size-4 渲染
  icon: LucideIcon
}

// 导航分组：组 id（稳定标识）+ 组标题 i18n 键 + 组内叶子。
export interface NavGroup {
  // 组稳定 id（仅作 React key 与稳定标识用）
  id: string
  // 组标题 i18n 键
  labelKey: string
  // 组内导航叶子
  leaves: NavLeaf[]
}

// 5 组分组树（顺序即侧栏从上到下的呈现顺序）：
// 概览 / 配置管理 / 集群 / 可观测 / 系统。
export const NAV_GROUPS: NavGroup[] = [
  {
    id: 'overview',
    labelKey: 'nav.groupOverview',
    leaves: [{ to: '/dashboard', labelKey: 'nav.dashboard', icon: LayoutDashboard }],
  },
  {
    id: 'config',
    labelKey: 'nav.groupConfig',
    leaves: [
      { to: '/configs', labelKey: 'nav.configs', icon: SlidersHorizontal },
      { to: '/file-preview', labelKey: 'nav.filePreview', icon: FolderTree },
      { to: '/imprint', labelKey: 'nav.imprint', icon: Stamp },
      { to: '/reverse-fetch', labelKey: 'nav.reverseFetchTask', icon: DownloadCloud },
    ],
  },
  {
    id: 'cluster',
    labelKey: 'nav.groupCluster',
    leaves: [
      { to: '/servers', labelKey: 'nav.servers', icon: Server },
      { to: '/topology', labelKey: 'nav.topology', icon: Network },
      { to: '/zones', labelKey: 'nav.zones', icon: MapPin },
    ],
  },
  {
    id: 'observability',
    labelKey: 'nav.groupObservability',
    leaves: [
      { to: '/service-analysis', labelKey: 'nav.serviceAnalysis', icon: ChartLine },
      { to: '/commands', labelKey: 'nav.commandObservability', icon: Terminal },
      { to: '/audits', labelKey: 'nav.audits', icon: ScrollText },
      { to: '/alert-events', labelKey: 'nav.alertEvents', icon: Bell },
    ],
  },
  {
    id: 'system',
    labelKey: 'nav.groupSystem',
    // 系统组 5 个扁平独立页（ADR-0048 取代 ADR-0043 的设置聚合 / 折叠 / 二级子 tab）：
    // 运维设置 / 版本与更新 / 控制面健康 / 密钥管理 / 环境管理，各自独立路由。
    leaves: [
      { to: '/settings', labelKey: 'nav.settings', icon: Settings },
      { to: '/system/version', labelKey: 'nav.versionUpdate', icon: RefreshCw },
      { to: '/system', labelKey: 'nav.systemObservability', icon: Activity },
      { to: '/api-keys', labelKey: 'nav.apiKeys', icon: KeyRound },
      { to: '/namespaces', labelKey: 'nav.namespaces', icon: Layers },
    ],
  },
]

// 扁平叶子集合（CommandPalette 导航目标消费）：按分组顺序铺平。
export const NAV_LEAVES: NavLeaf[] = NAV_GROUPS.flatMap((g) => g.leaves)
