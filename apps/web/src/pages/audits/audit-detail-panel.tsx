// 审计详情面板内容（非模态右侧列，非 Sheet）：单条审计全字段展示 + 与 /commands 互跳（FR-157）。
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { ArrowUpRight } from 'lucide-react'

import { Badge } from '@beacon/ui'
import type { AuditItem } from '@beacon/contracts'

interface AuditDetailPanelProps {
  // 展示的审计行
  item: AuditItem
}

export default function AuditDetailPanel({ item }: AuditDetailPanelProps) {
  const { t } = useTranslation()
  return (
    <div className="grid gap-3 text-sm">
      <Field label={t('observability.audits.columns.action')} value={item.action} mono />
      <Field label={t('observability.audits.columns.time')} value={new Date(item.createdAt).toLocaleString()} />
      <Field label={t('observability.audits.columns.operator')} value={item.operator} />
      <Field label={t('observability.audits.columns.targetType')} value={item.targetType} />
      <Field label={t('observability.audits.columns.targetRef')} value={item.targetRef} mono />
      <div className="grid gap-1">
        <span className="text-xs text-ink-4">{t('observability.audits.columns.result')}</span>
        <Badge className="w-fit" variant={item.result === 'ok' ? 'ok' : 'crit'}>
          {t(`observability.audits.result.${item.result}`)}
        </Badge>
      </div>
      <Field label={t('observability.audits.columns.clientIp')} value={item.clientIp} mono />
      <div className="grid gap-1">
        <span className="text-xs text-ink-4">{t('observability.audits.columns.detail')}</span>
        <p className="rounded-lg bg-secondary/60 px-2.5 py-2 text-xs text-ink-2">{item.detail}</p>
      </div>
      <Link
        className="inline-flex w-fit items-center gap-0.5 text-xs text-brand-600 hover:underline"
        to={`/commands?serverId=${item.targetRef}`}
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
