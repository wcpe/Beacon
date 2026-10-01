// 调用流水的语义呈现（列表与详情共用）：结果 / 风险等级药丸、原因标签与「危险」判定。
//
// 危险判定（PRD FR-241 的硬要求口径）= 被拒（result=rejected）或关键风险（riskLevel=critical）：
// 「被拒」说明执行面发生过门禁 / 业务拒绝，「关键风险」是不可逆、影响控制面自身或可提权的工具档位，
// 两者都在页面上一眼可辨——危险行整行走危险底色，避免只靠阅读枚举文字去筛。
//
// 与筛选维度（结果 / 风险等级两个下拉）互补：筛选负责把集合收窄，颜色负责把注意力拉过去。
import { useTranslation } from 'react-i18next'

import { Badge } from '@beacon/ui'
import type {
  MCPInvocationItem,
  MCPInvocationReason,
  MCPInvocationResult,
  MCPInvocationRiskLevel,
} from '@beacon/contracts'

/** 危险行标记：整行危险底色 + 首列左侧危险色竖条（不铺满整页红，只把危险行挑出来） */
export const DANGER_ROW_CLASS = 'bg-crit-bg/40 [&>td:first-child]:border-l-2 [&>td:first-child]:border-l-crit'

/** 危险判定：被拒或关键风险（两档都是执行面安全观测的高信号）。 */
export function isDangerous(row: MCPInvocationItem): boolean {
  return row.result === 'rejected' || row.riskLevel === 'critical'
}

// 结果 → 药丸语义：成功绿；失败 / 被拒红。
// 「被拒」是已处理的拒绝（handler 主动拒绝或执行面拒执，error 为 nil），但仍是需要关注的负向结果。
export function ResultBadge({ result }: { result: MCPInvocationResult }) {
  const { t } = useTranslation()
  return (
    <Badge variant={result === 'ok' ? 'ok' : 'crit'}>
      {t(`system.mcpInvocations.result.${result}`)}
    </Badge>
  )
}

// 风险等级 → 药丸语义：关键红（最高信号）；高位琥珀；低位弱色；未登记（撞目录）琥珀提示。
export function RiskBadge({ level }: { level: MCPInvocationRiskLevel }) {
  const { t } = useTranslation()
  const tone = level === 'critical' ? 'crit' : level === 'high' || level === 'unknown' ? 'warn' : 'off'
  return <Badge variant={tone}>{t(`system.mcpInvocations.riskLevel.${level}`)}</Badge>
}

/**
 * 原因标签：`result=ok` 恒为空串（成功无原因），此处回落为占位符；
 * 后端枚举若新增取值，用 defaultValue 回退原文，避免界面出现裸 i18n key。
 */
export function ReasonLabel({ reason }: { reason: MCPInvocationReason }) {
  const { t } = useTranslation()
  if (reason === '') {
    return <span className="text-xs text-ink-4">{t('system.mcpInvocations.dash')}</span>
  }
  return (
    <span className="text-xs text-ink-2">
      {t(`system.mcpInvocations.reason.${reason}`, { defaultValue: reason })}
    </span>
  )
}
