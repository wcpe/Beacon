// 轮换 / 重新启用的申请确认弹窗：两者都是「提审」语义（202），
// 需填写审批原因；轮换还会在批准后产出一次性明文 secret。
import { useEffect, useState } from 'react'
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

// 两种申请共用同一表单，仅文案与提交语义不同
export type ReasonAction = 'rotate' | 'enable'

interface ReasonDialogProps {
  // 当前动作；null 表示关闭
  action: ReasonAction | null
  // 目标客户端显示名（用于标题插值）
  targetName: string
  pending: boolean
  errorText: string | null
  onOpenChange: (open: boolean) => void
  onSubmit: (reason: string) => void
}

export default function ReasonDialog({
  action,
  targetName,
  pending,
  errorText,
  onOpenChange,
  onSubmit,
}: ReasonDialogProps) {
  const { t } = useTranslation()
  const [reason, setReason] = useState('')

  // 每次切换动作清空草稿
  useEffect(() => {
    if (action !== null) {
      setReason('')
    }
  }, [action])

  const open = action !== null
  const canSubmit = reason.trim() !== '' && !pending
  const prefix = action === 'enable' ? 'system.mcpClients.enable' : 'system.mcpClients.rotate'

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t(`${prefix}Title`, { name: targetName })}</DialogTitle>
          <DialogDescription>{t(`${prefix}Desc`)}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-1.5">
          <Label htmlFor="mcp-client-reason-input">{t('system.mcpClients.reasonLabel')}</Label>
          <Textarea
            id="mcp-client-reason-input"
            value={reason}
            onChange={(e) => {
              setReason(e.target.value)
            }}
            placeholder={t(`${prefix}ReasonPlaceholder`)}
            rows={2}
          />
        </div>
        {errorText && <p className="text-sm text-destructive">{errorText}</p>}
        <DialogFooter>
          <Button
            disabled={!canSubmit}
            onClick={() => {
              onSubmit(reason.trim())
            }}
          >
            {pending ? t('system.mcpClients.creating') : t(`${prefix}Confirm`)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
