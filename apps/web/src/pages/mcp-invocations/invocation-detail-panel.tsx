// 调用详情面板（FR-241）：按 invocationId 拉取单条流水（契约 §3.7 详情端点，后端按 ID 内嵌毫秒直定日表）。
//
// 为什么要二次取数而不是直接用列表行：详情端点是行点击的权威数据源（列表行可能来自上一页的
// keepPreviousData 快照），且未命中 / 非法 ID 会被后端一律判 404——面板需要如实呈现这一路径。
//
// 文案口径（契约级不变量）：targetDigest / argKeys 都是**脱敏摘要**——前者是白名单目标标识键的
// k=v 拼接，后者是顶层参数键名清单（内容类键只记字节数）。参数正文与结果正文不入库，界面不得
// 暗示「可以展开看到参数原文」，故面板固定展示 digestNotice 说明。
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'

import { AsyncSection, Skeleton } from '@beacon/ui'
import type { MCPInvocationItem } from '@beacon/contracts'

import { fetchMCPInvocation } from '../../api/mcp'
import { formatIso } from '../../features/system/format'
import { ReasonLabel, ResultBadge, RiskBadge } from './invocation-visuals'

export default function InvocationDetailPanel({ invocationId }: { invocationId: string }) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['mcp-invocations', 'detail', invocationId],
    queryFn: () => fetchMCPInvocation(invocationId),
  })

  return (
    <AsyncSection
      error={query.error}
      isError={query.isError}
      isLoading={query.isLoading}
      loadingText={t('system.mcpInvocations.loadFail')}
      skeleton={<Skeleton className="h-40 w-full" />}
    >
      {query.data === undefined ? null : <InvocationFields item={query.data} />}
    </AsyncSection>
  )
}

// 字段全集（契约 §3.2 逐项对应）：空串字段原样回落占位符，不隐藏行，避免「字段不存在」的误读。
function InvocationFields({ item }: { item: MCPInvocationItem }) {
  const { t } = useTranslation()
  const dash = t('system.mcpInvocations.dash')
  const show = (value: string): string => (value === '' ? dash : value)
  const fields: [string, string][] = [
    ['invocationId', item.invocationId],
    ['createdAt', formatIso(item.createdAt)],
    ['toolName', item.toolName],
    ['clientId', item.clientId],
    ['profile', t(`system.mcpClients.profile.${item.profile}`)],
    ['result', t(`system.mcpInvocations.result.${item.result}`)],
    ['riskLevel', t(`system.mcpInvocations.riskLevel.${item.riskLevel}`)],
    ['reason', item.reason === '' ? dash : t(`system.mcpInvocations.reason.${item.reason}`, { defaultValue: item.reason })],
    ['targetDigest', show(item.targetDigest)],
    ['argKeys', show(item.argKeys)],
    ['argBytes', t('system.mcpInvocations.byteCount', { count: item.argBytes })],
    ['durationMs', t('system.mcpInvocations.durationMs', { count: item.durationMs })],
    ['traceId', show(item.traceId)],
    ['clientIp', show(item.clientIp)],
    ['errorSummary', show(item.errorSummary)],
  ]

  return (
    <div className="grid gap-3 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <ResultBadge result={item.result} />
        <RiskBadge level={item.riskLevel} />
        <ReasonLabel reason={item.reason} />
      </div>
      {/* 脱敏口径常驻：摘要 ≠ 正文，避免运维误以为「点开能看到参数」 */}
      <p className="rounded-lg border border-border bg-surface-2 px-3 py-2 text-xs text-ink-3">
        {t('system.mcpInvocations.digestNotice')}
      </p>
      <dl className="grid gap-1.5">
        {fields.map(([key, value]) => (
          <div key={key} className="flex items-baseline justify-between gap-3">
            <dt className="shrink-0 text-xs text-ink-4">{t(`system.mcpInvocations.fields.${key}`)}</dt>
            <dd className="truncate text-right font-mono text-xs text-ink-1" title={value}>
              {value}
            </dd>
          </div>
        ))}
      </dl>
    </div>
  )
}
