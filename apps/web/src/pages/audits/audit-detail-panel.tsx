// 审计详情面板内容（非模态右侧列，非 Sheet）：单条审计全字段展示 + 与 /commands 互跳（FR-157）。
// detail 若为 JSON 则键值可视化（JsonDetail），否则按原文展示。
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { ArrowUpRight } from 'lucide-react'

import { Badge } from '@beacon/ui'
import type { AuditItem } from '@beacon/contracts'

import JsonDetail from '../../features/observability/json-detail'

interface AuditDetailPanelProps {
  // 展示的审计行
  item: AuditItem
}

export default function AuditDetailPanel({ item }: AuditDetailPanelProps) {
  const { t } = useTranslation()
  return (
    <div className="grid gap-3 text-sm">
      {/* 动作 / 目标类型：中文标签（未映射经 defaultValue 回退原始枚举） */}
      <Field
        label={t('observability.audits.columns.action')}
        value={t(`observability.audits.action.${item.action}`, { defaultValue: item.action })}
      />
      <Field label={t('observability.audits.columns.time')} value={new Date(item.createdAt).toLocaleString()} />
      <Field label={t('observability.audits.columns.operator')} value={item.operator} />
      <Field
        label={t('observability.audits.columns.targetType')}
        value={t(`observability.audits.targetTypeLabel.${item.targetType}`, {
          defaultValue: item.targetType,
        })}
      />
      <Field label={t('observability.audits.columns.targetRef')} value={item.targetRef} mono />
      <div className="grid gap-1">
        <span className="text-xs text-ink-4">{t('observability.audits.columns.result')}</span>
        <Badge className="w-fit" variant={item.result === 'ok' ? 'ok' : 'crit'}>
          {t(`observability.audits.result.${item.result}`, { defaultValue: item.result })}
        </Badge>
      </div>
      <Field label={t('observability.audits.columns.clientIp')} value={item.clientIp} mono />
      <JsonDetail
        raw={item.detail}
        title={t('observability.audits.columns.detail')}
        keyPrefix="observability.audits.detailKeys"
      />
      <Link
        className="inline-flex w-fit items-center gap-0.5 text-xs text-brand-600 hover:underline"
        to={`/commands?serverId=${encodeURIComponent(item.targetRef)}`}
      >
        {t('observability.audits.viewInCommands')}
        <ArrowUpRight className="size-3" />
      </Link>
    </div>
  )
}

// 单个只读字段（标签 + 值）
function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="grid gap-1">
      <span className="text-xs text-ink-4">{label}</span>
      <span className={mono ? 'font-mono text-xs text-ink-2' : 'text-sm text-ink-1'}>{value}</span>
    </div>
  )
}
