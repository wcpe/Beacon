// 健康因子「不适用原因」推导单测（FR-228 §4）：穷举因子 × 角色矩阵，锁定
// 「角色本就不适用（role）」与「未上报 / 不可得（missing）」的区分。
//
// 期望值来源：FR-32 §4.4 因子适用矩阵 + apps/server/internal/service/health_calc.go 的
// applicable 判定（已由后端 TestFactorApplicabilityByKind / TestFactorUnavailableSentinels 固化）。
import { describe, expect, it } from 'vitest'

import { inapplicableReasonOf } from '../../pages/servers/health-factor-reason'

describe('inapplicableReasonOf', () => {
  it('tps 仅 backend 适用 → proxy 上是角色不适用', () => {
    expect(inapplicableReasonOf('tps', 'proxy')).toBe('role')
  })

  it('conn 仅 proxy 适用 → backend 上是角色不适用', () => {
    expect(inapplicableReasonOf('conn', 'backend')).toBe('role')
  })

  it('capacity 仅 backend 适用：proxy 上是角色不适用', () => {
    expect(inapplicableReasonOf('capacity', 'proxy')).toBe('role')
  })

  it('capacity 在 backend 上不适用只可能是未上报（maxOnline<=0）', () => {
    // backend 恒适用 capacity，故不适用 = agent 未上报容量上限，需运维排查
    expect(inapplicableReasonOf('capacity', 'backend')).toBe('missing')
  })

  it('latency / cpu 两者皆适用 → 不适用只可能是数据不可得', () => {
    expect(inapplicableReasonOf('latency', 'backend')).toBe('missing')
    expect(inapplicableReasonOf('latency', 'proxy')).toBe('missing')
    expect(inapplicableReasonOf('cpu', 'backend')).toBe('missing')
    expect(inapplicableReasonOf('cpu', 'proxy')).toBe('missing')
  })

  it('未知因子名按「数据不可得」保守归类，不误报为角色不适用', () => {
    // 未来新增因子时，宁提示运维排查，也不静默说成「角色不适用」而掩盖问题
    expect(inapplicableReasonOf('some_future_factor', 'backend')).toBe('missing')
  })
})
