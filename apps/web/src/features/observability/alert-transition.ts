// 告警健康流转解析：从 detail JSON / message 抽出 from→to，供列表 / 通知 / 详情共用。
// 后端 message 形如 "game1 degraded → lost"（英文枚举）；展示时必须 i18n 健康态标签。

import type { AlertEventItem } from '@beacon/contracts'

export interface HealthTransition {
  from: string
  to: string
}

/** 解析健康流转 from→to；detail JSON 优先，其次 message 内箭头。 */
export function parseHealthTransition(item: AlertEventItem): HealthTransition | null {
  const raw = item.detail.trim()
  if (raw.startsWith('{')) {
    try {
      const obj = JSON.parse(raw) as Record<string, unknown>
      const from =
        (typeof obj.prevStatus === 'string' && obj.prevStatus) ||
        (typeof obj.fromStatus === 'string' && obj.fromStatus) ||
        (typeof obj.fromLevel === 'string' && obj.fromLevel) ||
        ''
      const to =
        (typeof obj.status === 'string' && obj.status) ||
        (typeof obj.toStatus === 'string' && obj.toStatus) ||
        (typeof obj.toLevel === 'string' && obj.toLevel) ||
        ''
      if (from !== '' && to !== '') {
        return { from, to }
      }
    } catch {
      // 非 JSON 走 message
    }
  }
  const match = /([A-Za-z_\u4e00-\u9fff]+)\s*(?:→|->)\s*([A-Za-z_\u4e00-\u9fff]+)/.exec(
    item.message,
  )
  if (match) {
    return { from: match[1], to: match[2] }
  }
  return null
}

// i18n t 最小签名：兼容 useTranslation().t 与 vitest 桩
// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Translate = (key: string, opts?: any) => string

/** 健康态枚举 → 文案（有 i18n 用中文/英文，否则原文） */
export function healthStatusLabel(t: Translate, value: string): string {
  return t(`observability.alertEvents.healthStatus.${value}`, { defaultValue: value })
}

/** 通知/列表副标题：优先「亚健康 → 失联」；无流转则回退 type 文案或 message */
export function alertSubtitle(item: AlertEventItem, t: Translate): string {
  const tr = parseHealthTransition(item)
  if (tr !== null) {
    const from = healthStatusLabel(t, tr.from)
    const to = healthStatusLabel(t, tr.to)
    return t('observability.alertEvents.transitionArrow', { from, to })
  }
  const typeKey = `observability.alertEvents.type.${item.type}`
  const typeLabel = t(typeKey, { defaultValue: '' })
  if (typeLabel !== '' && typeLabel !== typeKey) {
    return typeLabel
  }
  return item.message || '—'
}
