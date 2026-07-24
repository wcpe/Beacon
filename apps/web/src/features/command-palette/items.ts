// 命令面板条目构建 / 过滤 / 分组（FR-193）：纯函数，便于单测。
// MVP 以导航为主；可选 server 关键词与审计动作快捷项。

import { ALL_PAGES, NAV_GROUPS, DASHBOARD_PAGE, type NavPage } from '../../routes'

/** 结果分组 */
export type CommandGroup = 'nav' | 'servers' | 'audits'

export interface CommandItem {
  id: string
  group: CommandGroup
  /** i18n 键或已解析标题（导航用 titleKey，其它用 title） */
  titleKey?: string
  title?: string
  subtitle?: string
  to: string
}

/** 常见审计动作快捷项（与审计列表候选对齐的子集） */
export const AUDIT_ACTION_SHORTCUTS = [
  'auth.login',
  'identity.approved',
  'identity.unbound',
  'server.assign',
  'server.unassign',
  'delivery.order.start',
  'alert-event.resolve',
  'message.payload.view',
] as const

function navItem(page: NavPage, groupLabelKey: string): CommandItem {
  return {
    id: `nav:${page.path}`,
    group: 'nav',
    titleKey: page.titleKey,
    subtitle: groupLabelKey,
    to: page.path,
  }
}

/** 构建全量导航项（含运维总览 + 四大域） */
export function buildNavItems(): CommandItem[] {
  const items: CommandItem[] = [navItem(DASHBOARD_PAGE, 'nav.groups.ops')]
  for (const group of NAV_GROUPS) {
    for (const page of group.pages) {
      items.push(navItem(page, group.titleKey))
    }
  }
  return items
}

/** 审计动作快捷项 */
export function buildAuditActionItems(): CommandItem[] {
  return AUDIT_ACTION_SHORTCUTS.map((action) => ({
    id: `audit:${action}`,
    group: 'audits' as const,
    title: action,
    subtitle: 'audits',
    to: `/audits?action=${encodeURIComponent(action)}`,
  }))
}

/**
 * 若查询像 serverId（字母数字 / 短横线，长度 ≥2），追加「在服务器中搜索」项。
 */
export function buildServerSearchItem(query: string): CommandItem | null {
  const q = query.trim()
  if (q.length < 2) {
    return null
  }
  // 排除明显是中文导航关键词；允许字母数字与 -_.
  if (!/^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$/.test(q)) {
    return null
  }
  return {
    id: `server-search:${q}`,
    group: 'servers',
    title: q,
    subtitle: 'servers',
    to: `/servers?keyword=${encodeURIComponent(q)}`,
  }
}

export type ResolveTitle = (item: CommandItem) => string

/**
 * 过滤：空 query 只返回导航（避免噪声）；非空时匹配 title / titleKey 解析后的文案 / subtitle / to。
 */
export function filterItems(
  items: readonly CommandItem[],
  query: string,
  resolveTitle: ResolveTitle,
): CommandItem[] {
  const q = query.trim().toLowerCase()
  if (q === '') {
    return items.filter((item) => item.group === 'nav')
  }
  return items.filter((item) => {
    const title = resolveTitle(item).toLowerCase()
    const sub = (item.subtitle ?? '').toLowerCase()
    const path = item.to.toLowerCase()
    return title.includes(q) || sub.includes(q) || path.includes(q)
  })
}

/** 按 group 归类并保持组内顺序 */
export function groupItems(items: readonly CommandItem[]): {
  group: CommandGroup
  items: CommandItem[]
}[] {
  const order: CommandGroup[] = ['nav', 'servers', 'audits']
  const map = new Map<CommandGroup, CommandItem[]>()
  for (const item of items) {
    const list = map.get(item.group) ?? []
    list.push(item)
    map.set(item.group, list)
  }
  return order
    .filter((g) => (map.get(g)?.length ?? 0) > 0)
    .map((g) => ({ group: g, items: map.get(g) ?? [] }))
}

/**
 * 组装当前查询下的完整结果列表（导航 + 可选 server/审计）。
 */
export function buildPaletteItems(query: string, resolveTitle: ResolveTitle): CommandItem[] {
  const base = [...buildNavItems(), ...buildAuditActionItems()]
  const server = buildServerSearchItem(query)
  if (server !== null) {
    base.push(server)
  }
  return filterItems(base, query, resolveTitle)
}

/** 导出 ALL_PAGES 供测试对照（导航页数） */
export function navPageCount(): number {
  return ALL_PAGES.length
}
