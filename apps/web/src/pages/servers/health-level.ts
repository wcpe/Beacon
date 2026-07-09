// 健康等级展示映射：/servers 列表行与健康详情抽屉共用，避免两处各自复制。

import type { HealthLevel } from '@beacon/ui'

// 健康等级（healthy/degraded/unhealthy）→ 设计语言等级（决定分值配色）。
export const LEVEL_META: Record<string, HealthLevel> = {
  healthy: 'ok',
  degraded: 'warn',
  unhealthy: 'danger',
}

// 健康等级 → 药丸语义变体。
export function badgeOf(level: HealthLevel): 'ok' | 'warn' | 'crit' | 'off' {
  if (level === 'ok') {
    return 'ok'
  }
  if (level === 'warn') {
    return 'warn'
  }
  if (level === 'danger') {
    return 'crit'
  }
  return 'off'
}
