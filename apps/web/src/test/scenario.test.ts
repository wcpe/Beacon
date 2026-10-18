import {
  MOCK_SCENARIOS,
  MOCK_DATA_VOLUMES,
  MOCK_LATENCIES,
  getMockScenario,
  getMockDataVolume,
  getMockLatency,
  setMockScenario,
  setMockDataVolume,
  setMockLatency,
  subscribeMockScenario,
  subscribeMockDataVolume,
  subscribeMockLatency,
} from '@beacon/devmock'
import { afterEach, describe, expect, it, vi } from 'vitest'

// mock 场景四态基建冒烟：切换、持久化、订阅通知
describe('mock 场景切换', () => {
  afterEach(() => {
    setMockScenario('normal')
    setMockDataVolume('baseline')
    setMockLatency('none')
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

  it('数据量与延时是独立档位，并各自持久化', () => {
    expect(MOCK_DATA_VOLUMES.map((option) => [option.value, option.itemCount])).toEqual([
      ['baseline', null],
      ['small', 10],
      ['normal', 50],
      ['large', 500],
      ['xlarge', 5000],
    ])
    expect(MOCK_LATENCIES.map((option) => option.milliseconds)).toEqual([0, 200, 800, 2500])

    setMockDataVolume('large')
    setMockLatency('slow')

    expect(getMockDataVolume()).toBe('large')
    expect(window.localStorage.getItem('beacon-mock-data-volume')).toBe('large')
    expect(getMockLatency()).toBe('slow')
    expect(window.localStorage.getItem('beacon-mock-latency')).toBe('slow')
  })

  it('数据量与延时切换分别通知订阅者', () => {
    const volumeListener = vi.fn()
    const latencyListener = vi.fn()
    const unsubscribeVolume = subscribeMockDataVolume(volumeListener)
    const unsubscribeLatency = subscribeMockLatency(latencyListener)

    setMockDataVolume('small')
    setMockLatency('fast')

    expect(volumeListener).toHaveBeenCalledWith('small')
    expect(latencyListener).toHaveBeenCalledWith('fast')
    unsubscribeVolume()
    unsubscribeLatency()
  })
})
