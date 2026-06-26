// 环境全局 store 单测（FR-105）：默认值、setEnvironment 写入 + 读取、localStorage 持久化与恢复、
// 跨订阅广播、隐私模式写失败不崩。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

// 每个用例前清掉 localStorage 并重置模块（store 的 snapshot 在模块加载时初始化）
beforeEach(() => {
  localStorage.clear()
  vi.resetModules()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('环境 store 默认与持久化', () => {
  it('无持久化时回退默认（空串＝全部环境）', async () => {
    const { currentEnvironment } = await import('./environment')
    expect(currentEnvironment()).toBe('')
  })

  it('setEnvironment 写入 localStorage 并可在重载后恢复', async () => {
    const m = await import('./environment')
    m.setEnvironment('prod')
    // 已写入 localStorage（直接存字符串）
    expect(localStorage.getItem('beacon.environment')).toBe('prod')
    // 内存快照即时更新
    expect(m.currentEnvironment()).toBe('prod')
    // 重新加载模块：从 localStorage 恢复快照
    vi.resetModules()
    const reloaded = await import('./environment')
    expect(reloaded.currentEnvironment()).toBe('prod')
  })

  it('切回空串（全部环境）也持久化', async () => {
    const m = await import('./environment')
    m.setEnvironment('test')
    m.setEnvironment('')
    expect(localStorage.getItem('beacon.environment')).toBe('')
    expect(m.currentEnvironment()).toBe('')
  })
})

describe('环境 store 订阅广播', () => {
  it('setEnvironment 跨订阅者广播：useEnvironment 的多个使用方同步更新', async () => {
    const { renderHook, act } = await import('@testing-library/react')
    const m = await import('./environment')
    // 两个独立挂载的 hook 实例代表两处使用方（如页眉 EnvSelector 与某页筛选）
    const a = renderHook(() => m.useEnvironment())
    const b = renderHook(() => m.useEnvironment())
    expect(a.result.current).toBe('')
    expect(b.result.current).toBe('')
    // 任一处 setter 触发，所有订阅者同步收到新值
    act(() => m.setEnvironment('prod'))
    expect(a.result.current).toBe('prod')
    expect(b.result.current).toBe('prod')
  })
})

describe('隐私模式写失败不崩', () => {
  it('localStorage.setItem 抛错时 setter 仍更新内存快照不抛', async () => {
    const m = await import('./environment')
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('隐私模式禁止写入')
    })
    // 不应抛出
    expect(() => m.setEnvironment('prod')).not.toThrow()
    // 内存快照仍更新（仅持久化失败）
    expect(m.currentEnvironment()).toBe('prod')
  })
})
