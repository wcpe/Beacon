// 反向抓取任务进度纯逻辑单测（FR-87）：卡死判定 + 时长格式化穷举。

import { describe, it, expect } from 'vitest'

import {
  STUCK_THRESHOLD_SEC,
  formatElapsed,
  isTaskActive,
  isTaskStuck,
} from './reverseFetchProgress'

describe('isTaskActive', () => {
  it('非终态判活', () => {
    for (const s of ['scanning', 'pending-review', 'fetching', 'conflict-review', 'ingesting']) {
      expect(isTaskActive(s)).toBe(true)
    }
  })
  it('终态判否', () => {
    for (const s of ['done', 'failed', 'cancelled', 'expired']) {
      expect(isTaskActive(s)).toBe(false)
    }
  })
})

describe('isTaskStuck', () => {
  it('非终态且超阈值 → 卡死', () => {
    expect(isTaskStuck('scanning', STUCK_THRESHOLD_SEC)).toBe(true)
    expect(isTaskStuck('fetching', STUCK_THRESHOLD_SEC + 100)).toBe(true)
  })
  it('非终态但未超阈值 → 不卡死', () => {
    expect(isTaskStuck('scanning', STUCK_THRESHOLD_SEC - 1)).toBe(false)
    expect(isTaskStuck('fetching', 0)).toBe(false)
  })
  it('终态无论多久都不卡死', () => {
    expect(isTaskStuck('done', 99999)).toBe(false)
    expect(isTaskStuck('failed', 99999)).toBe(false)
    expect(isTaskStuck('expired', 99999)).toBe(false)
  })
})

describe('formatElapsed', () => {
  it('秒级', () => {
    expect(formatElapsed(0)).toBe('0s')
    expect(formatElapsed(59)).toBe('59s')
  })
  it('分钟级', () => {
    expect(formatElapsed(60)).toBe('1m')
    expect(formatElapsed(125)).toBe('2m')
    expect(formatElapsed(3599)).toBe('59m')
  })
  it('小时级', () => {
    expect(formatElapsed(3600)).toBe('1h')
    expect(formatElapsed(3660)).toBe('1h 1m')
    expect(formatElapsed(7320)).toBe('2h 2m')
  })
  it('负值归零', () => {
    expect(formatElapsed(-5)).toBe('0s')
  })
})
