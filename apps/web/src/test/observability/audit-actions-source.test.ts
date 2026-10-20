// 审计动作清单同源守护（issue #152）
//
// 背景：审计页动作筛选曾是一份手写常量清单（57 项），后端 model.Action* 已增至 157 项，
// 手写清单必然滞后——运维在下拉里选不到 instance.tail-logs / file.browse / asset.diff 等动作，
// 只能手工拼 ?action=。现改为「生成物」方案：
//   enums.go ──scripts/gen-audit-actions.mjs──▶ apps/web/src/pages/audits/audit-actions.generated.ts
// 本文件守护生成物与真源一致，避免「有人改了后端枚举却忘记重新生成」这类漂移悄悄上线。
//
// 漂移如何被抓到：
//   后端新增/改名/删除 Action* → 生成物集合与真源集合不等 → 本文件第一组用例变红，
//   diff 会直接列出缺失与多余的动作；修复方式是 pnpm gen:audit-actions 而不是手改清单。
//   若有人手改生成物（改顺序 / 加手写项）→ 集合比对或前缀连续性用例同样变红。
import { describe, expect, it } from 'vitest'

import { AUDIT_ACTIONS } from '../../pages/audits/audit-actions.generated'
import { ENUMS_GO_PATH, actionPrefixOf, readBackendActions } from './enums-source'

const BACKEND_ACTIONS = readBackendActions()
const GENERATED: readonly string[] = AUDIT_ACTIONS

describe('审计动作生成物：真源可读性（防空转）', () => {
  it('enums.go 可定位且动作数量在合理量级', () => {
    expect(ENUMS_GO_PATH).toContain('enums.go')
    // 下限 100：远低于当前真实值（157），但足以在正则 / 路径失配时立刻变红
    expect(BACKEND_ACTIONS.length).toBeGreaterThan(100)
    // 真源自身无重复字面值（重复会让「集合比对」掩盖掉数量差异）
    expect(new Set(BACKEND_ACTIONS).size).toBe(BACKEND_ACTIONS.length)
  })
})

describe('审计动作生成物与 enums.go 同步', () => {
  it('生成物未漏掉任何后端动作', () => {
    const known = new Set(GENERATED)
    const missing = BACKEND_ACTIONS.filter((action) => !known.has(action))
    expect(
      missing,
      `以下后端动作不在审计页动作筛选候选里（运维将选不到）——请运行 pnpm gen:audit-actions：\n${missing.join('\n')}`,
    ).toEqual([])
  })

  it('生成物不含后端已不存在的动作（避免筛选出永远空结果）', () => {
    const backend = new Set(BACKEND_ACTIONS)
    const extra = GENERATED.filter((action) => !backend.has(action))
    expect(
      extra,
      `以下动作已不在后端枚举中（筛选结果必然为空）——请运行 pnpm gen:audit-actions：\n${extra.join('\n')}`,
    ).toEqual([])
  })

  it('生成物数量与真源一致且自身无重复', () => {
    expect(GENERATED.length).toBe(BACKEND_ACTIONS.length)
    expect(new Set(GENERATED).size).toBe(GENERATED.length)
  })
})

describe('审计动作生成物的顺序结构（前缀分组）', () => {
  // 按前缀连续分组：同域动作（config.* / file.* / delivery.* …）必须相邻，
  // 否则 157 项在下拉里是散乱长列表，键入过滤之外无法逐域扫读。
  const runs = GENERATED.reduce<string[]>((acc, action) => {
    const prefix = actionPrefixOf(action)
    if (acc[acc.length - 1] !== prefix) {
      acc.push(prefix)
    }
    return acc
  }, [])

  it('每个动作前缀只占一个连续区间（没有跨域断开又回来）', () => {
    const duplicated = runs.filter((prefix, index) => runs.indexOf(prefix) !== index)
    expect(
      duplicated,
      `以下前缀在候选列表里被切成了多个不连续区间（生成物被手改过？请运行 pnpm gen:audit-actions）：\n${duplicated.join('\n')}`,
    ).toEqual([])
  })

  it('确实形成了多个分组（否则上面的连续性断言没有意义）', () => {
    expect(runs.length).toBeGreaterThan(10)
    // 分组数不可能多于动作数
    expect(runs.length).toBeLessThanOrEqual(GENERATED.length)
  })

  it('组内顺序与 enums.go 声明顺序一致（生成器不额外排序，便于按后端演进对照）', () => {
    const offenders: string[] = []
    for (const prefix of runs) {
      const expected = BACKEND_ACTIONS.filter((action) => actionPrefixOf(action) === prefix)
      const actual = GENERATED.filter((action) => actionPrefixOf(action) === prefix)
      if (expected.join('|') !== actual.join('|')) {
        offenders.push(`${prefix}: 生成物 [${actual.join(', ')}] / 真源 [${expected.join(', ')}]`)
      }
    }
    expect(offenders, `以下分组在生成物里被重新排序过（生成物应保持 enums.go 的组内声明顺序）：\n${offenders.join('\n')}`).toEqual(
      [],
    )
  })
})
