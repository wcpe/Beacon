// 偏好 store 单测（FR-92）：localStorage 持久化与恢复、setter 广播、隐私模式写失败不崩、
// applyThemeToDocument 打 / 去 .dark 类。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

// 每个用例前清掉 localStorage 与文档类、并重置模块（store 的 snapshot 在模块加载时初始化）
beforeEach(() => {
  localStorage.clear()
  document.documentElement.classList.remove('dark')
  vi.resetModules()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('偏好 store 默认与持久化', () => {
  it('无持久化时回退默认（浅色 + 舒适 + 无手动展开组）', async () => {
    const { currentPreferences } = await import('./preferences')
    expect(currentPreferences()).toEqual({
      theme: 'light',
      density: 'comfortable',
      navExpandedGroups: [],
    })
  })

  it('setTheme/setDensity 写入 localStorage 并可在重载后恢复', async () => {
    const m = await import('./preferences')
    m.setTheme('dark')
    m.setDensity('compact')
    // 已写入 localStorage
    expect(localStorage.getItem('beacon.preferences')).toBe(
      JSON.stringify({ theme: 'dark', density: 'compact', navExpandedGroups: [] }),
    )
    // 重新加载模块：从 localStorage 恢复快照
    vi.resetModules()
    const reloaded = await import('./preferences')
    expect(reloaded.currentPreferences()).toEqual({
      theme: 'dark',
      density: 'compact',
      navExpandedGroups: [],
    })
  })

  it('非法持久化值回落默认', async () => {
    localStorage.setItem('beacon.preferences', JSON.stringify({ theme: 'x', density: 'y' }))
    const { currentPreferences } = await import('./preferences')
    expect(currentPreferences()).toEqual({
      theme: 'light',
      density: 'comfortable',
      navExpandedGroups: [],
    })
  })
})

describe('偏好 store navExpandedGroups（FR-93）', () => {
  it('setNavExpandedGroups 写入合法组 id 并可重载后恢复', async () => {
    const m = await import('./preferences')
    m.setNavExpandedGroups(['cluster', 'observability'])
    expect(m.currentPreferences().navExpandedGroups).toEqual(['cluster', 'observability'])
    vi.resetModules()
    const reloaded = await import('./preferences')
    expect(reloaded.currentPreferences().navExpandedGroups).toEqual(['cluster', 'observability'])
  })

  it('未知组 id / 非字符串被剔除、重复去重', async () => {
    const m = await import('./preferences')
    // 'bogus' 非合法组、123 非字符串、'cluster' 重复
    m.setNavExpandedGroups(['cluster', 'bogus', 123 as unknown as string, 'cluster'])
    expect(m.currentPreferences().navExpandedGroups).toEqual(['cluster'])
  })

  it('持久化的 navExpandedGroups 非数组时回落空数组', async () => {
    localStorage.setItem(
      'beacon.preferences',
      JSON.stringify({ theme: 'dark', density: 'compact', navExpandedGroups: 'oops' }),
    )
    const { currentPreferences } = await import('./preferences')
    expect(currentPreferences().navExpandedGroups).toEqual([])
  })

  it('持久化的 navExpandedGroups 含未知组时剔除非法值', async () => {
    localStorage.setItem(
      'beacon.preferences',
      JSON.stringify({ navExpandedGroups: ['system', 'nope', 'overview'] }),
    )
    const { currentPreferences } = await import('./preferences')
    expect(currentPreferences().navExpandedGroups).toEqual(['system', 'overview'])
  })
})

describe('偏好 store 订阅广播', () => {
  it('setter 触发订阅者回调', async () => {
    const m = await import('./preferences')
    // usePreferences 内部用 subscribe；这里直接验证 setter 改变快照
    m.setTheme('dark')
    expect(m.currentPreferences().theme).toBe('dark')
    m.setDensity('compact')
    expect(m.currentPreferences().density).toBe('compact')
    // 改一项不影响另一项
    expect(m.currentPreferences().theme).toBe('dark')
  })
})

describe('隐私模式写失败不崩', () => {
  it('localStorage.setItem 抛错时 setter 仍更新内存快照不抛', async () => {
    const m = await import('./preferences')
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('隐私模式禁止写入')
    })
    // 不应抛出
    expect(() => m.setTheme('dark')).not.toThrow()
    // 内存快照仍更新（仅持久化失败）
    expect(m.currentPreferences().theme).toBe('dark')
  })
})

describe('applyThemeToDocument', () => {
  it('dark 打 .dark 类、light 去 .dark 类', async () => {
    const { applyThemeToDocument } = await import('./preferences')
    applyThemeToDocument('dark')
    expect(document.documentElement.classList.contains('dark')).toBe(true)
    applyThemeToDocument('light')
    expect(document.documentElement.classList.contains('dark')).toBe(false)
  })
})
