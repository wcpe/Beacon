// 交付审计动作 i18n 覆盖守护：delivery.order.* 全部动作必须有非空中文映射，
// 防审计页「动作」列出裸英文 key（映射机制：observability.audits.action + defaultValue 回退，
// 与 Legacy web 的 audit.action 覆盖守护同思路）。新增 / 删除 delivery 审计动作时同步改本清单。
import { describe, expect, it } from 'vitest'

import { observability } from '../../i18n/observability'

// 交付编排域 16 个审计动作（真源：后端 delivery 审计枚举）
const DELIVERY_AUDIT_ACTIONS = [
  'delivery.order.create',
  'delivery.order.update',
  'delivery.order.delete',
  'delivery.order.submit',
  'delivery.order.withdraw',
  'delivery.order.approve',
  'delivery.order.reject',
  'delivery.order.start',
  'delivery.order.pause',
  'delivery.order.resume',
  'delivery.order.batch_confirm',
  'delivery.order.cancel',
  'delivery.order.rollback',
  'delivery.order.rollback_finish',
  'delivery.order.circuit_break',
  'delivery.order.blob_cleanup',
] as const

describe('delivery 审计动作 i18n 映射完整性守护', () => {
  const map: Record<string, string> = observability.audits.action

  it.each(DELIVERY_AUDIT_ACTIONS)('action「%s」有非空中文映射', (action) => {
    expect(
      map[action],
      `action「${action}」缺 observability.audits.action 中文映射——审计页「动作」列会显原始英文 key`,
    ).toBeTruthy()
  })
})
