// 命令详情面板内容（非模态右侧列）：单条命令双向生命周期（下发 → 取走 → 回执）全字段。
// 只读元数据，永不展示命令 payload / 回执明文。与 /audits 互跳（FR-157）。
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { ArrowUpRight } from 'lucide-react'

import { Badge } from '@beacon/ui'
import type { CommandItem } from '@beacon/devmock'

// 命令状态 → 状态药丸语义色：done 正常绿、failed/expired 危急红、其余次要。
function badgeVariant(status: CommandItem['status']): 'ok' | 'off' | 'crit' {
  if (status === 'failed' || status === 'expired') {
    return 'crit'
  }
  if (status === 'done') {
    return 'ok'
  }
  return 'off'
}

interface CommandDetailPanelProps {
  // 展示的命令行
  item: CommandItem
}

export default function CommandDetailPanel({ item }: CommandDetailPanelProps) {
  const { t } = useTranslation()
  return (
    <div className="grid gap-3 text-sm">
      <Field label={t('observability.commands.columns.commandId')} value={String(item.commandId)} mono />
      <div className="grid gap-1">
        <span className="text-xs text-ink-4">{t('observability.commands.columns.status')}</span>
        <Badge className="w-fit" variant={badgeVariant(item.status)}>
          {t(`observability.commands.status.${item.status}`)}
        </Badge>
      </div>
      <Field label={t('observability.commands.columns.serverId')} value={item.serverId} mono />
      <Field label={t('observability.commands.columns.type')} value={item.type} />
      <Field label={t('observability.commands.columns.operator')} value={item.operator} />

      {/* 双向生命周期时间线：下发 → 最近更新（取走 / 回执） */}
      <div className="grid gap-1">
        <span className="text-xs text-ink-4">{t('observability.commands.lifecycle')}</span>
        <div className="grid gap-1 rounded-lg bg-secondary/50 px-2.5 py-2 text-xs text-ink-2">
          <div className="flex items-center justify-between gap-2">
            <span className="text-ink-4">{t('observability.commands.columns.createdAt')}</span>
            <span className="tabular-nums">{new Date(item.createdAt).toLocaleString()}</span>
          </div>
          <div className="flex items-center justify-between gap-2">
            <span className="text-ink-4">{t('observability.commands.updatedAt')}</span>
            <span className="tabular-nums">{new Date(item.updatedAt).toLocaleString()}</span>
          </div>
          <div className="flex items-center justify-between gap-2">
            <span className="text-ink-4">{t('observability.commands.columns.age')}</span>
            <span className="tabular-nums">{t('observability.commands.ageSeconds', { count: item.ageSeconds })}</span>
          </div>
        </div>
      </div>

      <div className="grid gap-1">
        <span className="text-xs text-ink-4">{t('observability.commands.columns.result')}</span>
        <p className="rounded-lg bg-secondary/60 px-2.5 py-2 text-xs text-ink-2">{item.resultDetail || '—'}</p>
      </div>

      <Link
        className="inline-flex w-fit items-center gap-0.5 text-xs text-brand-600 hover:underline"
        to={`/audits?targetRef=${item.serverId}`}
      >
        {t('observability.commands.viewInAudits')}
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
