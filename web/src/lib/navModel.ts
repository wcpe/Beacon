// 侧栏导航单一真源（FR-93）：把导航分组树与扁平叶子集中到一处，
// Layout 按分组树形渲染 5 组手风琴、CommandPalette 取扁平叶子做导航目标——消除两份重复清单的漂移。
// 纯数据 + 派生，无副作用、可单测。

// 单个导航叶子：路由 + i18n 文案键。
export interface NavLeaf {
  to: string
  labelKey: string
}

// 导航分组：组 id（用于偏好持久化与自动展开判定）+ 组标题 i18n 键 + 组内叶子。
export interface NavGroup {
  // 组稳定 id（持久化到偏好的 navExpandedGroups 用此值）
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
    leaves: [{ to: '/dashboard', labelKey: 'nav.dashboard' }],
  },
  {
    id: 'config',
    labelKey: 'nav.groupConfig',
    leaves: [
      { to: '/configs', labelKey: 'nav.configs' },
      { to: '/file-preview', labelKey: 'nav.filePreview' },
      { to: '/imprint', labelKey: 'nav.imprint' },
      { to: '/reverse-fetch', labelKey: 'nav.reverseFetchTask' },
    ],
  },
  {
    id: 'cluster',
    labelKey: 'nav.groupCluster',
    leaves: [
      { to: '/servers', labelKey: 'nav.servers' },
      { to: '/topology', labelKey: 'nav.topology' },
      { to: '/zones', labelKey: 'nav.zones' },
    ],
  },
  {
    id: 'observability',
    labelKey: 'nav.groupObservability',
    leaves: [
      { to: '/service-analysis', labelKey: 'nav.serviceAnalysis' },
      { to: '/audits', labelKey: 'nav.audits' },
      { to: '/alert-events', labelKey: 'nav.alertEvents' },
    ],
  },
  {
    id: 'system',
    labelKey: 'nav.groupSystem',
    leaves: [{ to: '/settings', labelKey: 'nav.settings' }],
  },
]

// 全部合法分组 id（偏好层校验 navExpandedGroups 时用，剔除未知值）。
export const NAV_GROUP_IDS: string[] = NAV_GROUPS.map((g) => g.id)

// 扁平叶子集合（CommandPalette 导航目标消费）：按分组顺序铺平。
export const NAV_LEAVES: NavLeaf[] = NAV_GROUPS.flatMap((g) => g.leaves)

// 判断某分组是否命中当前路由（组内任一叶子是 pathname 的前缀即命中）——命中组自动展开。
export function isGroupActive(group: NavGroup, pathname: string): boolean {
  return group.leaves.some((leaf) => pathname === leaf.to || pathname.startsWith(`${leaf.to}/`))
}
