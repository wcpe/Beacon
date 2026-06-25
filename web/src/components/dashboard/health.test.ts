// health 工具单测：状态→等级映射、比率双阈值定级、计数阈值定级（看板/健康页上色的判据，须穷举边界）。
import { describe, it, expect } from 'vitest'
import { statusLevel, ratioLevel, countLevel } from './health'

describe('statusLevel', () => {
  it('online→ok / degraded→warn / lost→danger / offline→muted', () => {
    expect(statusLevel('online')).toBe('ok')
    expect(statusLevel('degraded')).toBe('warn')
    expect(statusLevel('lost')).toBe('danger')
    expect(statusLevel('offline')).toBe('muted')
  })
  it('未知状态保守按 warn', () => {
    expect(statusLevel('weird')).toBe('warn')
  })
})

describe('ratioLevel（默认 warn=0.7 / danger=0.9）', () => {
  it('低占比为 ok，跨阈值依次升级', () => {
    expect(ratioLevel(0.5)).toBe('ok')
    expect(ratioLevel(0.7)).toBe('warn')
    expect(ratioLevel(0.85)).toBe('warn')
    expect(ratioLevel(0.9)).toBe('danger')
    expect(ratioLevel(1)).toBe('danger')
  })
  it('非法占比（负 / NaN）按 muted', () => {
    expect(ratioLevel(-1)).toBe('muted')
    expect(ratioLevel(Number.NaN)).toBe('muted')
  })
})

describe('countLevel（默认 danger=∞）', () => {
  it('0 为 ok，>0 标 warn（默认无危险阈值）', () => {
    expect(countLevel(0)).toBe('ok')
    expect(countLevel(1)).toBe('warn')
    expect(countLevel(999)).toBe('warn')
  })
  it('给定危险阈值后达阈值标 danger', () => {
    expect(countLevel(5, 5)).toBe('danger')
    expect(countLevel(4, 5)).toBe('warn')
  })
})
