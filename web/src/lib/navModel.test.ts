// 导航单一真源单测（FR-93，方案 A 分组常驻 + 图标）：锁定 5 组结构、扁平叶子与每个叶子带图标。
import { describe, it, expect } from 'vitest'
import { NAV_GROUPS, NAV_LEAVES } from './navModel'

describe('navModel 分组结构', () => {
  it('恰为 5 组，顺序为 概览/配置管理/集群/可观测/系统', () => {
    expect(NAV_GROUPS).toHaveLength(5)
    expect(NAV_GROUPS.map((g) => g.id)).toEqual(['overview', 'config', 'cluster', 'observability', 'system'])
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

  it('三页合一后（FR-113）配置组只剩工作台 + 文件树预览，无拓印 / 反向抓取叶子', () => {
    const tos = NAV_LEAVES.map((l) => l.to)
    expect(tos).not.toContain('/imprint')
    expect(tos).not.toContain('/reverse-fetch')
    const config = NAV_GROUPS.find((g) => g.id === 'config')!
    expect(config.leaves.map((l) => l.to)).toEqual(['/configs', '/file-preview'])
  })

  it('每个叶子都配了语义图标（方案 A）', () => {
    for (const leaf of NAV_LEAVES) {
      // lucide-react 图标是可渲染组件（forwardRef 对象或函数）
      expect(leaf.icon).toBeTruthy()
      expect(['object', 'function']).toContain(typeof leaf.icon)
    }
  })
})
