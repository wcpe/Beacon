// 告警处理弹窗：确认（acknowledged，无需备注）/ 标记已处理（resolved，备注必填）。
// 写失败时在弹窗内联展示脱敏错误（Shell 未挂 Toaster）。

import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Button,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Label,
  Textarea,
} from '@beacon/ui'

// 处理意图：确认或标记已处理
export type HandleIntent = 'acknowledged' | 'resolved'

interface HandleDialogProps {
  // 处理意图；null 表示关闭
  intent: HandleIntent | null
  // 处理中
  pending: boolean
  // 脱敏错误文案（内联展示）
  errorText: string | null
  onOpenChange: (open: boolean) => void
  // 确认回调（resolved 时携带备注）
  onConfirm: (note: string) => void
}

export default function HandleDialog({
  intent,
  pending,
  errorText,
  onOpenChange,
  onConfirm,
}: HandleDialogProps) {
  const { t } = useTranslation()
  const [note, setNote] = useState('')
  const needNote = intent === 'resolved'
  const canConfirm = !needNote || note.trim() !== ''

  return (
    <Dialog
      open={intent !== null}
      onOpenChange={(open) => {
        if (!open) {
          setNote('')
        }
        onOpenChange(open)
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('observability.alertEvents.handleTitle')}</DialogTitle>
          <DialogDescription>
            {needNote
              ? t('observability.alertEvents.handleResolveDesc')
              : t('observability.alertEvents.handleAckDesc')}
          </DialogDescription>
        </DialogHeader>

        {needNote && (
          <div className="grid gap-1.5">
            <Label htmlFor="handle-note">{t('observability.alertEvents.note')}</Label>
            <Textarea
              id="handle-note"
              value={note}
              placeholder={t('observability.alertEvents.notePlaceholder')}
              onChange={(e) => {
                setNote(e.target.value)
              }}
            />
          </div>
        )}

        {errorText !== null && <p className="text-sm text-destructive">{errorText}</p>}

        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => {
              onOpenChange(false)
            }}
          >
            {t('observability.alertEvents.cancel')}
          </Button>
          <Button
            disabled={!canConfirm || pending}
            onClick={() => {
              onConfirm(note.trim())
            }}
          >
            {needNote
              ? t('observability.alertEvents.confirmResolve')
              : t('observability.alertEvents.confirmAck')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
