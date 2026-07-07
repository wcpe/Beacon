// 审计详情抽屉（Sheet）：单条审计全字段展示 + 与 /commands 互跳（FR-157）。
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'

import {
  Badge,
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@beacon/ui'
import type { AuditItem } from '@beacon/devmock'

interface AuditDetailSheetProps {
  // 打开的审计行；null 表示关闭
  item: AuditItem | null
  onOpenChange: (open: boolean) => void
}

export default function AuditDetailSheet({ item, onOpenChange }: AuditDetailSheetProps) {
  const { t } = useTranslation()
  return (
    <Sheet open={item !== null} onOpenChange={onOpenChange}>
      <SheetContent className="w-full gap-0 overflow-y-auto sm:max-w-md">
        <SheetHeader>
          <SheetTitle>{t('observability.audits.detailTitle')}</SheetTitle>
          <SheetDescription>{item?.action}</SheetDescription>
        </SheetHeader>
        {item && (
          <div className="grid gap-3 px-4 pb-6 text-sm">
            <Field label={t('observability.audits.columns.time')} value={new Date(item.createdAt).toLocaleString()} />
            <Field label={t('observability.audits.columns.operator')} value={item.operator} />
            <Field label={t('observability.audits.columns.targetType')} value={item.targetType} />
            <Field label={t('observability.audits.columns.targetRef')} value={item.targetRef} mono />
            <div className="grid gap-1">
              <span className="text-xs text-muted-foreground">{t('observability.audits.columns.result')}</span>
              <Badge variant={item.result === 'ok' ? 'secondary' : 'destructive'}>
                {t(`observability.audits.result.${item.result}`)}
              </Badge>
            </div>
            <Field label={t('observability.audits.columns.clientIp')} value={item.clientIp} mono />
            <div className="grid gap-1">
              <span className="text-xs text-muted-foreground">{t('observability.audits.columns.detail')}</span>
              <p className="rounded-md bg-muted px-2 py-1.5 text-xs">{item.detail}</p>
            </div>
            <Link
              className="text-xs text-primary hover:underline"
              to={`/commands?serverId=${item.targetRef}`}
            >
              {t('observability.audits.viewInCommands')}
            </Link>
          </div>
        )}
      </SheetContent>
    </Sheet>
  )
}

// 单个只读字段（标签 + 值）
function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="grid gap-1">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className={mono ? 'font-mono text-xs' : 'text-sm'}>{value}</span>
    </div>
  )
}
