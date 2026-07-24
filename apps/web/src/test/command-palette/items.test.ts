// 命令面板纯函数单测（FR-193）
import { describe, expect, it } from 'vitest'

import {
  buildAuditActionItems,
  buildNavItems,
  buildPaletteItems,
  buildServerSearchItem,
  filterItems,
  groupItems,
  navPageCount,
  type CommandItem,
} from '../../features/command-palette/items'

const resolveTitle = (item: CommandItem): string => item.title ?? item.titleKey ?? item.id

describe('buildNavItems', () => {
  it('覆盖全站导航页数', () => {
    expect(buildNavItems()).toHaveLength(navPageCount())
  })

  it('含运维总览与 servers', () => {
    const paths = buildNavItems().map((i) => i.to)
    expect(paths).toContain('/dashboard')
    expect(paths).toContain('/servers')
    expect(paths).toContain('/audits')
  })
})

describe('filterItems', () => {
  const items = buildNavItems()

  it('空 query 只返回导航', () => {
    const mixed: CommandItem[] = [
      ...items,
      { id: 'a', group: 'audits', title: 'auth.login', to: '/audits?action=auth.login' },
    ]
    const filtered = filterItems(mixed, '', resolveTitle)
    expect(filtered.every((i) => i.group === 'nav')).toBe(true)
  })

  it('按 titleKey 子串过滤', () => {
    // 用 to 路径匹配（titleKey 原样含 nav.servers）
    const filtered = filterItems(items, 'servers', resolveTitle)
    expect(filtered.some((i) => i.to === '/servers')).toBe(true)
  })
})

describe('buildServerSearchItem', () => {
  it('短查询不生成', () => {
    expect(buildServerSearchItem('a')).toBeNull()
  })

  it('合法 serverId 生成深链', () => {
    const item = buildServerSearchItem('lobby-1')
    expect(item).not.toBeNull()
    expect(item?.to).toBe('/servers?keyword=lobby-1')
    expect(item?.group).toBe('servers')
  })

  it('中文不生成 server 项', () => {
    expect(buildServerSearchItem('服务器')).toBeNull()
  })
})

describe('buildAuditActionItems', () => {
  it('动作跳 audits?action=', () => {
    const items = buildAuditActionItems()
    expect(items.length).toBeGreaterThan(0)
    expect(items[0].to).toMatch(/^\/audits\?action=/)
  })
})

describe('groupItems / buildPaletteItems', () => {
  it('分组顺序 nav → servers → audits', () => {
    const items = buildPaletteItems('login', resolveTitle)
    const groups = groupItems(items).map((g) => g.group)
    // 可能只有 audits；有则顺序正确
    for (let i = 1; i < groups.length; i += 1) {
      const order = ['nav', 'servers', 'audits']
      expect(order.indexOf(groups[i])).toBeGreaterThan(order.indexOf(groups[i - 1]))
    }
  })

  it('空查询只有导航', () => {
    const items = buildPaletteItems('', resolveTitle)
    expect(items.every((i) => i.group === 'nav')).toBe(true)
    expect(items.length).toBe(navPageCount())
  })
})
