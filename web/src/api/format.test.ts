// format 工具纯函数单测：聚焦运行时长格式化（FR-33 页眉用）的正常 / 边界 / 错误路径。
import { describe, it, expect } from 'vitest'
import { formatDuration } from './format'

describe('formatDuration', () => {
  it('非法 / 负数 / 非有限值回退为 -', () => {
    expect(formatDuration(undefined)).toBe('-')
    expect(formatDuration(null)).toBe('-')
    expect(formatDuration(-1)).toBe('-')
    expect(formatDuration(Number.NaN)).toBe('-')
    expect(formatDuration(Number.POSITIVE_INFINITY)).toBe('-')
  })

  it('不足 1 秒显示 0 秒', () => {
    expect(formatDuration(0)).toBe('0 秒')
    expect(formatDuration(0.4)).toBe('0 秒')
  })

  it('秒 / 分 / 时 / 天 各量级', () => {
    expect(formatDuration(45)).toBe('45 秒')
    expect(formatDuration(90)).toBe('1 分 30 秒')
    expect(formatDuration(3600)).toBe('1 小时')
    expect(formatDuration(3 * 3600 + 25 * 60)).toBe('3 小时 25 分')
    expect(formatDuration(86400)).toBe('1 天')
  })

  it('最多取最高两个量级（忽略更低量级）', () => {
    // 2 天 3 小时 5 分 7 秒 → 仅取「2 天 3 小时」
    expect(formatDuration(2 * 86400 + 3 * 3600 + 5 * 60 + 7)).toBe('2 天 3 小时')
  })
})
