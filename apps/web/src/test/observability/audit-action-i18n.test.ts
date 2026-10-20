// 审计动作 i18n 覆盖守护（真源驱动）：以后端 model.Action* 常量为唯一真源，
// 断言「后端会写入审计的每个 action」在 zh 与 en 两份 locale 里都有非空映射——
// 防审计页动作列 / 详情面板 / 命令面板 / KPI「最高频动作」露出裸 i18n key 或英文枚举。
//
// 此前用人工硬编码清单只覆盖 29 个动作，后端新增动作不会让测试变红（漏测严重）；
// 现改为读 apps/server/internal/model/enums.go 正则提取，新增常量即纳入守护。
// 「定位并解析 enums.go」的逻辑抽到 ./enums-source，与动作清单同源守护（audit-actions-source.test.ts）共用。
import i18next, { type TFunction } from 'i18next'
import { beforeAll, describe, expect, it } from 'vitest'

import { observability } from '../../i18n/observability'
import { observability as enObservability } from '../../i18n/en/observability'
import { ENUMS_GO_PATH, readBackendActions } from './enums-source'

/** 审计 action i18n key 前缀（与 features/observability/audit-action-label.ts 保持一致） */
const ACTION_KEY_PREFIX = 'observability.audits.action'

/** 后端全部审计动作字面值（真源：model.Action* = "..."） */
const BACKEND_ACTIONS = readBackendActions()
const zhActionMap: Record<string, string> = observability.audits.action
const enActionMap: Record<string, string> = enObservability.audits.action

// 独立 i18next 实例：不受全局 i18n 初始化顺序与当前语言影响，可同时取 zh / en 译文
let tZh: TFunction
let tEn: TFunction

beforeAll(async () => {
  const instance = i18next.createInstance()
  await instance.init({
    lng: 'zh-CN',
    fallbackLng: 'zh-CN',
    interpolation: { escapeValue: false },
    resources: {
      'zh-CN': { translation: { observability } },
      en: { translation: { observability: enObservability } },
    },
  })
  tZh = instance.getFixedT('zh-CN')
  tEn = instance.getFixedT('en')
})

describe('审计动作真源（enums.go）提取', () => {
  it('真源可读且动作数量在合理量级（防正则失效导致守护空转）', () => {
    expect(ENUMS_GO_PATH).toContain('enums.go')
    // 下限 100：远低于当前真实值（155），但足以在正则 / 路径失配时立刻变红
    expect(BACKEND_ACTIONS.length).toBeGreaterThan(100)
    expect(new Set(BACKEND_ACTIONS).size).toBe(BACKEND_ACTIONS.length)
  })
})

describe('审计动作 zh 映射覆盖真源', () => {
  it('后端每个 Action 都有非空中文映射（缺一条即列出全部差集）', () => {
    const missing = BACKEND_ACTIONS.filter((action) => !zhActionMap[action])
    expect(
      missing,
      `以下审计动作缺 observability.audits.action 中文映射——审计页动作列 / KPI 最高频动作会显裸 key：\n${missing.join('\n')}`,
    ).toEqual([])
  })
})

describe('审计动作 en 映射覆盖真源', () => {
  it('后端每个 Action 都有非空英文映射（缺失会回退中文，造成中英混排）', () => {
    const missing = BACKEND_ACTIONS.filter((action) => !enActionMap[action])
    expect(
      missing,
      `以下审计动作缺 en observability.audits.action 映射（回退 zh-CN 造成中英混排）：\n${missing.join('\n')}`,
    ).toEqual([])
  })

  it('zh / en 动作键集合完全一致（不出现只补一侧的漂移）', () => {
    const zhOnly = Object.keys(zhActionMap).filter((key) => !enActionMap[key])
    const enOnly = Object.keys(enActionMap).filter((key) => !zhActionMap[key])
    expect({ zhOnly, enOnly }).toEqual({ zhOnly: [], enOnly: [] })
  })
})

describe('含点号 action 键在 i18next 下解析正确', () => {
  // 点号 key 极易被拆成对象路径（如 instance 下的 register）：必须用真实 t() 取一次值验证，
  // 只读对象只能证明数据在，不能证明运行时取得到。
  const dottedActions = BACKEND_ACTIONS.filter((action) => action.includes('.'))

  it('真源中存在含点号动作（否则本组用例空转）', () => {
    expect(dottedActions.length).toBeGreaterThan(50)
  })

  it.each(dottedActions)('action「%s」经 i18n.t() 取到非空译文且不含裸 key', (action) => {
    for (const [locale, t] of [
      ['zh-CN', () => tZh],
      ['en', () => tEn],
    ] as const) {
      const label = t()(`${ACTION_KEY_PREFIX}.${action}`)
      expect(label, `${locale} 未解析出「${action}」译文`).toBeTruthy()
      expect(label, `${locale} 的「${action}」回显了裸 i18n key`).not.toContain(ACTION_KEY_PREFIX)
      expect(label, `${locale} 的「${action}」回退成了原始枚举`).not.toBe(action)
      expect(label.trim(), `${locale} 的「${action}」译文为空白`).not.toBe('')
    }
  })
})

describe('未映射动作的回退语义', () => {
  it('未知 action 走 defaultValue 回退原文（显英文而非裸 key，不阻断新增动作展示）', () => {
    const unknown = 'not-a-real.action'
    expect(tZh(`${ACTION_KEY_PREFIX}.${unknown}`, { defaultValue: unknown })).toBe(unknown)
  })
})
