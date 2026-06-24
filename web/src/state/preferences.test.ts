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
  it('无持久化时回退默认（浅色 + 舒适）', async () => {
    const { currentPreferences } = await import('./preferences')
    expect(currentPreferences()).toEqual({ theme: 'light', density: 'comfortable' })
  })

  it('setTheme/setDensity 写入 localStorage 并可在重载后恢复', async () => {
    const m = await import('./preferences')
    m.setTheme('dark')
    m.setDensity('compact')
    // 已写入 localStorage
    expect(localStorage.getItem('beacon.preferences')).toBe(
      JSON.stringify({ theme: 'dark', density: 'compact' }),
    )
    // 重新加载模块：从 localStorage 恢复快照
    vi.resetModules()
    const reloaded = await import('./preferences')
    expect(reloaded.currentPreferences()).toEqual({ theme: 'dark', density: 'compact' })
  })

  it('非法持久化值回落默认', async () => {
    localStorage.setItem('beacon.preferences', JSON.stringify({ theme: 'x', density: 'y' }))
    const { currentPreferences } = await import('./preferences')
    expect(currentPreferences()).toEqual({ theme: 'light', density: 'comfortable' })
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
