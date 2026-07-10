// 变更单生命周期写操作确认弹窗：可选原因必填 + 可选恢复方式单选 + 内联脱敏错误。
// 承 UX §4 二次确认；原因 / 恢复方式按操作决定是否展示（reject/cancel 必填原因，
// 熔断恢复必填 mode+reason，普通迁移仅确认）。

import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  Label,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Textarea,
} from '@beacon/ui'

/** 恢复方式：熔断 / 准备失败暂停后继续需二选一 */
export type ResumeMode = 'retry_failed' | 'skip_failed'

export interface ConfirmResult {
  reason: string
  mode: ResumeMode | null
}

interface ConfirmDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description: string
  confirmLabel: string
  // 影响预览行
  impacts?: string[]
  // 是否要求填写原因
  requireReason?: boolean
  // 是否要求选择恢复方式（熔断 / 准备失败恢复）
  requireMode?: boolean
  pending: boolean
  // 脱敏错误文案（提交失败时内联展示，不静默隐藏）
  errorText?: string | null
  onConfirm: (result: ConfirmResult) => void
}

export default function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  impacts,
  requireReason = false,
  requireMode = false,
  pending,
  errorText,
  onConfirm,
}: ConfirmDialogProps) {
  const { t } = useTranslation()
  const [reason, setReason] = useState('')
  const [mode, setMode] = useState<ResumeMode>('retry_failed')

  // 每次打开清空草稿
  useEffect(() => {
    if (open) {
      setReason('')
      setMode('retry_failed')
    }
  }, [open])

  const reasonOk = !requireReason || reason.trim() !== ''
  const canConfirm = reasonOk && !pending

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>

        {impacts && impacts.length > 0 && (
          <ul className="list-disc space-y-1 rounded-md bg-muted/50 px-5 py-3 text-sm text-muted-foreground">
            {impacts.map((line, i) => (
              <li key={i}>{line}</li>
            ))}
          </ul>
        )}

        {requireMode && (
          <div className="space-y-1.5">
            <Label htmlFor="change-confirm-mode" className="text-sm font-medium">
              {t('delivery.changes.confirm.resumeMode')}
            </Label>
            <Select
              value={mode}
              onValueChange={(next) => {
                setMode(next as ResumeMode)
              }}
            >
              <SelectTrigger
                id="change-confirm-mode"
                aria-label={t('delivery.changes.confirm.resumeMode')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="retry_failed">{t('delivery.changes.confirm.retryFailed')}</SelectItem>
                <SelectItem value="skip_failed">{t('delivery.changes.confirm.skipFailed')}</SelectItem>
              </SelectContent>
            </Select>
          </div>
        )}

        {requireReason && (
          <div className="space-y-1.5">
            <Label htmlFor="change-confirm-reason">{t('cluster.servers.reason.label')}</Label>
            <Textarea
              id="change-confirm-reason"
              aria-label={t('cluster.servers.reason.label')}
              value={reason}
              onChange={(e) => {
                setReason(e.target.value)
              }}
              placeholder={t('cluster.servers.reason.placeholder')}
              rows={2}
            />
          </div>
        )}

        {errorText !== null && errorText !== undefined && errorText !== '' && (
          <p className="text-sm text-destructive">{errorText}</p>
        )}

        <AlertDialogFooter>
          <AlertDialogCancel>{t('delivery.changes.create.cancel')}</AlertDialogCancel>
          <AlertDialogAction
            disabled={!canConfirm}
            onClick={(e) => {
              // 阻止 radix 默认关闭，交由调用方在成功后关闭
              e.preventDefault()
              onConfirm({ reason: reason.trim(), mode: requireMode ? mode : null })
            }}
          >
            {confirmLabel}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
