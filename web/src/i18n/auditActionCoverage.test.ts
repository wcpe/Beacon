import { describe, it, expect } from 'vitest'

import { zhCN } from './locales/zh-CN'

// 审计动作枚举的权威真源是后端 `internal/model/enums.go` 的 Action* 常量。
// 下面的清单镜像之——**新增 / 删除 action 时必须同步改这里**。
// 本测试守护「每个审计 action 都有中文 i18n 映射」，防止审计日志页（FR-7）/ 服务分析页（FR-73）
// 的「动作」列直接显原始英文 key（曾发生过：反向抓取受管子动作落库却漏配映射）。
// 思路与后端 `coveredWriteRoutes` 漂移守护一致：用一份维护清单挡住「加了枚举忘了配套」的低级漂移。
const AUDIT_ACTIONS = [
  // config.*
  'config.create',
  'config.publish',
  'config.rollback',
  'config.delete',
  'config.disable',
  'config.enable',
  'config.gray-publish',
  'config.gray-promote',
  'config.gray-abort',
  // file.*
  'file.create',
  'file.publish',
  'file.rollback',
  'file.delete',
  'file.disable',
  'file.enable',
  'file.import',
  'file.imprint',
  'file.imprint-fetch',
  'file.reverse-fetch',
  'file.reverse-fetch-scan',
  'file.reverse-fetch-submit',
  'file.reverse-fetch-ingest',
  'file.reverse-fetch-cancel',
  'file.reverse-fetch-error',
  // reverse-fetch.* / override-set.*
  'reverse-fetch.ignore-rule-add',
  'reverse-fetch.ignore-rule-remove',
  'override-set.create',
  'override-set.publish',
  'override-set.rollback',
  'override-set.delete',
  // instance.* / zone.* / scheduling.*
  'instance.register',
  'instance.offline',
  'instance.online',
  'instance.resync',
  'zone.assign',
  'zone.move',
  'zone.unassign',
  'zone.set-default-entry',
  'zone.clear-default-entry',
  'scheduling.drain',
  'scheduling.undrain',
  // namespace.* / auth.* / apikey.* / settings.*
  'namespace.create',
  'namespace.update',
  'namespace.delete',
  'auth.login',
  'auth.logout',
  'apikey.create',
  'apikey.revoke',
  'apikey.reset',
  'settings.update',
] as const

describe('审计 action i18n 映射完整性守护（FR-7 / FR-73 漂移守护）', () => {
  const map = zhCN.audit.action as Record<string, string>

  it.each(AUDIT_ACTIONS)('action「%s」有非空中文映射', (action) => {
    expect(
      map[action],
      `action「${action}」缺 audit.action 中文映射——审计日志页 / 服务分析页会显原始英文 key，请在 zh-CN.ts 的 audit.action 补一条`,
    ).toBeTruthy()
  })

  it('audit.action 映射无清单外多余 key（删 action 时同步清理）', () => {
    const known = new Set<string>(AUDIT_ACTIONS)
    const extra = Object.keys(map).filter((k) => !known.has(k))
    expect(
      extra,
      `audit.action 有清单外的 key：${extra.join(', ')}——若删了对应 action 请同步清理本测试清单与映射`,
    ).toEqual([])
  })
})
