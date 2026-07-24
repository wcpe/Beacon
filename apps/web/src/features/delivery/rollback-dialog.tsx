// 整单回滚高风险确认弹窗（共享控件）：手输复述「回滚」+ 原因必填（承 UX §4 高风险二次确认 + 原因）。
// 手输不等于复述词则禁用确认；提交进行中禁用；内联脱敏错误。/changes 与历史页共用。
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
  Input,
  Label,
  Textarea,
} from '@beacon/ui'

interface RollbackDialogProps {
  open: boolean
  pending: boolean
  errorText: string | null
  onConfirm: (reason: string) => void
  onOpenChange: (open: boolean) => void
}

export default function RollbackDialog({
  open,
  pending,
  errorText,
  onConfirm,
  onOpenChange,
}: RollbackDialogProps) {
  const { t } = useTranslation()
  const phrase = t('delivery.rollback.phrase')
  const [typed, setTyped] = useState('')
  const [reason, setReason] = useState('')

  // 每次打开清空草稿
  useEffect(() => {
    if (open) {
      setTyped('')
      setReason('')
    }
  }, [open])

  const canConfirm = typed.trim() === phrase && reason.trim() !== '' && !pending

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t('delivery.rollback.title')}</AlertDialogTitle>
          <AlertDialogDescription>
            {t('delivery.rollback.desc')}
          </AlertDialogDescription>
        </AlertDialogHeader>

        <div className="space-y-1.5">
          <Label htmlFor="rollback-phrase">
            {t('delivery.rollback.phraseLabel', { phrase })}
          </Label>
          <Input
            id="rollback-phrase"
            aria-label={t('delivery.rollback.phraseLabel', { phrase })}
            value={typed}
            onChange={(e) => {
              setTyped(e.target.value)
            }}
          />
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="rollback-reason">
            {t('delivery.rollback.reasonLabel')}
          </Label>
          <Textarea
            id="rollback-reason"
            aria-label={t('delivery.rollback.reasonLabel')}
            value={reason}
            onChange={(e) => {
              setReason(e.target.value)
            }}
            placeholder={t('delivery.rollback.reasonPlaceholder')}
            rows={2}
          />
        </div>

        {errorText && <p className="text-sm text-destructive">{errorText}</p>}

        <AlertDialogFooter>
          <AlertDialogCancel>{t('delivery.changes.create.cancel')}</AlertDialogCancel>
          <AlertDialogAction
            disabled={!canConfirm}
            onClick={(e) => {
              e.preventDefault()
              onConfirm(reason.trim())
            }}
          >
            {t('delivery.rollback.confirm')}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
