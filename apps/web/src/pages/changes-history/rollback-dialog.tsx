// 整单回滚高风险确认弹窗：手输复述「回滚」+ 原因必填（承 UX §4 高风险二次确认 + 原因）。
// 手输不等于复述词则禁用确认；提交进行中禁用；内联脱敏错误。
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
  const phrase = t('delivery.changesHistory.rollback.phrase')
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
          <AlertDialogTitle>{t('delivery.changesHistory.rollback.title')}</AlertDialogTitle>
          <AlertDialogDescription>
            {t('delivery.changesHistory.rollback.desc')}
          </AlertDialogDescription>
        </AlertDialogHeader>

        <div className="space-y-1.5">
          <Label htmlFor="rollback-phrase">
            {t('delivery.changesHistory.rollback.phraseLabel', { phrase })}
          </Label>
          <Input
            id="rollback-phrase"
            aria-label={t('delivery.changesHistory.rollback.phraseLabel', { phrase })}
            value={typed}
            onChange={(e) => {
              setTyped(e.target.value)
            }}
          />
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="rollback-reason">
            {t('delivery.changesHistory.rollback.reasonLabel')}
          </Label>
          <Textarea
            id="rollback-reason"
            aria-label={t('delivery.changesHistory.rollback.reasonLabel')}
            value={reason}
            onChange={(e) => {
              setReason(e.target.value)
            }}
            placeholder={t('delivery.changesHistory.rollback.reasonPlaceholder')}
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
            {t('delivery.changesHistory.rollback.confirm')}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
