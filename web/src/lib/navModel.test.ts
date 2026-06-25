// 导航单一真源单测（FR-93）：锁定 5 组结构、扁平叶子与命中组判定。
import { describe, it, expect } from 'vitest'
import { NAV_GROUPS, NAV_GROUP_IDS, NAV_LEAVES, isGroupActive } from './navModel'

describe('navModel 分组结构', () => {
  it('恰为 5 组，顺序为 概览/配置管理/集群/可观测/系统', () => {
    expect(NAV_GROUPS).toHaveLength(5)
    expect(NAV_GROUP_IDS).toEqual(['overview', 'config', 'cluster', 'observability', 'system'])
  })

  it('扁平叶子覆盖各组全部路由且不丢项', () => {
    const total = NAV_GROUPS.reduce((n, g) => n + g.leaves.length, 0)
    expect(NAV_LEAVES).toHaveLength(total)
    // 关键路由都在扁平集合里（CommandPalette 消费）
    const tos = NAV_LEAVES.map((l) => l.to)
    expect(tos).toContain('/dashboard')
    expect(tos).toContain('/configs')
    expect(tos).toContain('/servers')
    expect(tos).toContain('/service-analysis')
    expect(tos).toContain('/settings')
  })
})

describe('isGroupActive 命中组判定', () => {
  const configGroup = NAV_GROUPS.find((g) => g.id === 'config')!
  const clusterGroup = NAV_GROUPS.find((g) => g.id === 'cluster')!

  it('叶子路由精确命中其所属组', () => {
    expect(isGroupActive(configGroup, '/configs')).toBe(true)
    expect(isGroupActive(clusterGroup, '/servers')).toBe(true)
  })

  it('子路径（叶子前缀 + /）也命中', () => {
    expect(isGroupActive(configGroup, '/configs/abc')).toBe(true)
  })

  it('非本组路由不命中', () => {
    expect(isGroupActive(configGroup, '/servers')).toBe(false)
    expect(isGroupActive(clusterGroup, '/configs')).toBe(false)
  })

  it('前缀相近但非子路径不误命中', () => {
    // '/configs-foo' 不是 '/configs' 的子路径
    expect(isGroupActive(configGroup, '/configs-foo')).toBe(false)
  })
})
