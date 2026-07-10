// 向导批次编排纯函数单测：推荐批次生成、总和校验、划分预览换算与目标数估算。
import { describe, expect, it } from 'vitest'

import {
  batchIssue,
  buildBatch,
  estimateTargetTotal,
  flattenZoneCounts,
  planBatchRows,
  recommendedBatch,
  type WizardBatch,
  type WizardScope,
} from '../../pages/changes/wizard-state'

const scopeOf = (partial: Partial<WizardScope>): WizardScope => ({
  mode: 'all',
  regions: [],
  zones: [],
  servers: [],
  ...partial,
})

describe('recommendedBatch 推荐批次', () => {
  it('目标数未知按百分比金丝雀 10/30/60', () => {
    expect(recommendedBatch(null)).toEqual({ mode: 'staged', unit: 'percent', rows: [10, 30, 60] })
  })

  it('单台目标一批收口', () => {
    expect(recommendedBatch(1)).toEqual({ mode: 'staged', unit: 'count', rows: [1] })
  })

  it('小目标数（≤5）均分两批', () => {
    expect(recommendedBatch(2).rows).toEqual([1, 1])
    expect(recommendedBatch(5).rows).toEqual([2, 3])
  })

  it('常规目标首批约一成（至少 1 台）、次批三成、末批余量', () => {
    expect(recommendedBatch(6).rows).toEqual([1, 2, 3])
    expect(recommendedBatch(100).rows).toEqual([10, 30, 60])
  })

  it('推荐结果总和恒等于目标数', () => {
    for (const total of [1, 2, 3, 5, 6, 7, 11, 47, 100, 1200]) {
      const sum = recommendedBatch(total).rows.reduce((acc, size) => acc + size, 0)
      expect(sum).toBe(total)
    }
  })
})

describe('batchIssue 总和校验', () => {
  const staged = (unit: WizardBatch['unit'], rows: number[]): WizardBatch => ({ mode: 'staged', unit, rows })

  it('一次性全量不校验', () => {
    expect(batchIssue({ mode: 'single', unit: 'percent', rows: [] }, 10)).toBeNull()
  })

  it('行值非法（<1 / 非整数 / 空）报 invalid_row', () => {
    expect(batchIssue(staged('count', []), 10)).toBe('invalid_row')
    expect(batchIssue(staged('count', [0, 5]), 10)).toBe('invalid_row')
    expect(batchIssue(staged('percent', [10.5, 89.5]), 10)).toBe('invalid_row')
  })

  it('百分比合计须等于 100', () => {
    expect(batchIssue(staged('percent', [10, 30, 60]), null)).toBeNull()
    expect(batchIssue(staged('percent', [10, 30]), null)).toBe('percent_sum')
    expect(batchIssue(staged('percent', [50, 60]), null)).toBe('percent_sum')
  })

  it('台数在目标数已知时合计须等于目标数', () => {
    expect(batchIssue(staged('count', [1, 2, 3]), 6)).toBeNull()
    expect(batchIssue(staged('count', [1, 2]), 6)).toBe('count_short')
    expect(batchIssue(staged('count', [5, 5]), 6)).toBe('count_over')
    // 目标数未知（树未加载）时不卡台数总和
    expect(batchIssue(staged('count', [1, 2]), null)).toBeNull()
  })
})

describe('planBatchRows 划分预览', () => {
  it('百分比逐批向上取整且不超过剩余，累计收敛到目标数', () => {
    const rows = planBatchRows({ mode: 'staged', unit: 'percent', rows: [10, 30, 60] }, 11)
    expect(rows.map((r) => r.count)).toEqual([2, 4, 5])
    expect(rows.at(-1)?.cumulative).toBe(11)
  })

  it('一次性全量视作 percent[100]', () => {
    const rows = planBatchRows({ mode: 'single', unit: 'count', rows: [1] }, 7)
    expect(rows).toEqual([{ batchNo: 1, size: 100, count: 7, cumulative: 7 }])
  })

  it('台数超出剩余时按剩余截断', () => {
    const rows = planBatchRows({ mode: 'staged', unit: 'count', rows: [5, 5] }, 6)
    expect(rows.map((r) => r.count)).toEqual([5, 1])
  })

  it('目标数未知时百分比行无法换算、台数行直显输入量', () => {
    expect(planBatchRows({ mode: 'staged', unit: 'percent', rows: [10] }, null)[0].count).toBeNull()
    const counted = planBatchRows({ mode: 'staged', unit: 'count', rows: [2, 3] }, null)
    expect(counted.map((r) => r.count)).toEqual([2, 3])
    expect(counted.at(-1)?.cumulative).toBe(5)
  })
})

describe('buildBatch 契约换算', () => {
  it('一次性 = percent[100]；分批按单位原样下发', () => {
    expect(buildBatch({ mode: 'single', unit: 'count', rows: [1, 2] })).toEqual({
      batchMode: 'percent',
      batchSizes: [100],
    })
    expect(buildBatch({ mode: 'staged', unit: 'count', rows: [1, 2, 3] })).toEqual({
      batchMode: 'count',
      batchSizes: [1, 2, 3],
    })
    expect(buildBatch({ mode: 'staged', unit: 'percent', rows: [10, 90] })).toEqual({
      batchMode: 'percent',
      batchSizes: [10, 90],
    })
  })
})

describe('estimateTargetTotal 目标数估算', () => {
  const tree = {
    clusters: [
      {
        regions: [
          { id: 20, zones: [{ id: 30, serverCount: 4 }, { id: 31, serverCount: 4 }] },
          { id: 21, zones: [{ id: 32, serverCount: 2 }, { id: 33, serverCount: 1 }] },
        ],
      },
    ],
  }
  const zones = flattenZoneCounts(tree)

  it('全量 = 全部小区台数和；按大区 / 小区取所选并集', () => {
    expect(estimateTargetTotal(scopeOf({ mode: 'all' }), zones)).toBe(11)
    expect(estimateTargetTotal(scopeOf({ mode: 'regions', regions: [20] }), zones)).toBe(8)
    expect(estimateTargetTotal(scopeOf({ mode: 'zones', zones: [30, 33] }), zones)).toBe(5)
  })

  it('单服模式恒为已选数；树未加载时其余模式未知', () => {
    expect(estimateTargetTotal(scopeOf({ mode: 'servers', servers: ['a', 'b'] }), undefined)).toBe(2)
    expect(estimateTargetTotal(scopeOf({ mode: 'all' }), undefined)).toBeNull()
  })
})
