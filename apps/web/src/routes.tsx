// 全站路由与导航信息架构配置（真源：docs/UX.md §2）：
// 顶层运维总览 + 集群 / 可观测 / 交付 / 系统四大域，共 17 页。
// 后续页面 agent 只改 src/pages/ 下自己的页面文件与 src/i18n/ 下所属域资源文件，勿改本文件结构。
import type { ComponentType } from 'react'
import {
  BellRing,
  Boxes,
  ChartLine,
  FileSliders,
  FolderArchive,
  GitPullRequestArrow,
  Globe,
  HeartPulse,
  History,
  KeyRound,
  LayoutDashboard,
  RefreshCw,
  ScrollText,
  Server,
  Settings,
  SquareTerminal,
  Workflow,
  type LucideIcon,
} from 'lucide-react'

import AlertEventsPage from './pages/alert-events'
import ApiKeysPage from './pages/api-keys'
import AssetsPage from './pages/assets'
import AuditsPage from './pages/audits'
import ChangesPage from './pages/changes'
import ChangesHistoryPage from './pages/changes-history'
import CommandsPage from './pages/commands'
import ConfigsPage from './pages/configs'
import DashboardPage from './pages/dashboard'
import NamespacesPage from './pages/namespaces'
import ServersPage from './pages/servers'
import ServiceAnalysisPage from './pages/service-analysis'
import SettingsPage from './pages/settings'
import SystemPage from './pages/system'
import SystemVersionPage from './pages/system-version'
import TopologyPage from './pages/topology'
import ZonesPage from './pages/zones'

export interface NavPage {
  // 路由路径（与 UX.md §2 一致）
  path: string
  // 页面标题的 i18n 键（nav 域，侧栏与页面骨架共用）
  titleKey: string
  // 侧栏图标
  icon: LucideIcon
  // 页面组件
  Component: ComponentType
}

export interface NavGroup {
  // 大域分组标题的 i18n 键（nav.groups 域）
  titleKey: string
  // 组内页面（顺序即侧栏展示顺序）
  pages: NavPage[]
}

// 顶层运维总览（不属于任何大域）
export const DASHBOARD_PAGE: NavPage = {
  path: '/dashboard',
  titleKey: 'nav.dashboard',
  icon: LayoutDashboard,
  Component: DashboardPage,
}

// 四大域分组（集群 / 可观测 / 交付 / 系统）
export const NAV_GROUPS: NavGroup[] = [
  {
    titleKey: 'nav.groups.cluster',
    pages: [
      { path: '/servers', titleKey: 'nav.servers', icon: Server, Component: ServersPage },
      { path: '/zones', titleKey: 'nav.zones', icon: Boxes, Component: ZonesPage },
      { path: '/topology', titleKey: 'nav.topology', icon: Workflow, Component: TopologyPage },
    ],
  },
  {
    titleKey: 'nav.groups.observability',
    pages: [
      {
        path: '/service-analysis',
        titleKey: 'nav.serviceAnalysis',
        icon: ChartLine,
        Component: ServiceAnalysisPage,
      },
      { path: '/commands', titleKey: 'nav.commands', icon: SquareTerminal, Component: CommandsPage },
      { path: '/audits', titleKey: 'nav.audits', icon: ScrollText, Component: AuditsPage },
      { path: '/alert-events', titleKey: 'nav.alertEvents', icon: BellRing, Component: AlertEventsPage },
    ],
  },
  {
    titleKey: 'nav.groups.delivery',
    pages: [
      { path: '/assets', titleKey: 'nav.assets', icon: FolderArchive, Component: AssetsPage },
      { path: '/configs', titleKey: 'nav.configs', icon: FileSliders, Component: ConfigsPage },
      { path: '/changes', titleKey: 'nav.changes', icon: GitPullRequestArrow, Component: ChangesPage },
      {
        path: '/changes/history',
        titleKey: 'nav.changesHistory',
        icon: History,
        Component: ChangesHistoryPage,
      },
    ],
  },
  {
    titleKey: 'nav.groups.system',
    pages: [
      { path: '/settings', titleKey: 'nav.settings', icon: Settings, Component: SettingsPage },
      { path: '/system', titleKey: 'nav.system', icon: HeartPulse, Component: SystemPage },
      {
        path: '/system/version',
        titleKey: 'nav.systemVersion',
        icon: RefreshCw,
        Component: SystemVersionPage,
      },
      { path: '/api-keys', titleKey: 'nav.apiKeys', icon: KeyRound, Component: ApiKeysPage },
      { path: '/namespaces', titleKey: 'nav.namespaces', icon: Globe, Component: NamespacesPage },
    ],
  },
]

// 全部页面的扁平列表（路由注册用）
export const ALL_PAGES: NavPage[] = [DASHBOARD_PAGE, ...NAV_GROUPS.flatMap((group) => group.pages)]
