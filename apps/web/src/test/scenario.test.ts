import {
  MOCK_SCENARIOS,
  getMockScenario,
  setMockScenario,
  subscribeMockScenario,
} from '@beacon/devmock'
import { afterEach, describe, expect, it, vi } from 'vitest'

// mock 场景四态基建冒烟：切换、持久化、订阅通知
describe('mock 场景切换', () => {
  afterEach(() => {
    setMockScenario('normal')
    window.localStorage.clear()
  })

  it('默认处于常规态且四态齐全', () => {
    expect(getMockScenario()).toBe('normal')
    expect(MOCK_SCENARIOS).toEqual(['empty', 'normal', 'huge', 'error'])
  })

  it('切换场景后可读回并持久化', () => {
    setMockScenario('huge')
    expect(getMockScenario()).toBe('huge')
    expect(window.localStorage.getItem('beacon-mock-scenario')).toBe('huge')
  })

  it('订阅者在切换时收到通知，退订后不再收到', () => {
    const listener = vi.fn()
    const unsubscribe = subscribeMockScenario(listener)
    setMockScenario('error')
    expect(listener).toHaveBeenCalledWith('error')
    unsubscribe()
    setMockScenario('empty')
    expect(listener).toHaveBeenCalledTimes(1)
  })

  it('重复设置同一场景不重复通知', () => {
    const listener = vi.fn()
    const unsubscribe = subscribeMockScenario(listener)
    setMockScenario('huge')
    setMockScenario('huge')
    expect(listener).toHaveBeenCalledTimes(1)
    unsubscribe()
  })
})
