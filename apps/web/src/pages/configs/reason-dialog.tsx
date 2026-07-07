// 配置中心域带原因输入的写操作确认弹窗（承 UX §4「高风险二次确认 + 原因输入」）：
// 影响预览 + 原因必填 + 提交进行中禁用 + 内联脱敏错误展示（ADR-0057，不静默隐藏）。
// 与集群域 reason-dialog 同构，交付域自持一份以保持所有权清晰。

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
  Textarea,
} from '@beacon/ui'

interface ReasonDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description: string
  confirmLabel: string
  // 影响预览行（撤销哪层 / 影响哪些服）
  impacts?: string[]
  pending: boolean
  // 展示的脱敏错误文案（提交失败时）
  errorText?: string | null
  // 确认回调：把原因回传给调用方触发写操作
  onConfirm: (reason: string) => void
}

export default function ReasonDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  impacts,
  pending,
  errorText,
  onConfirm,
}: ReasonDialogProps) {
  const { t } = useTranslation()
  const [reason, setReason] = useState('')

  // 每次打开清空原因草稿，避免残留上次输入
  useEffect(() => {
    if (open) {
      setReason('')
    }
  }, [open])

  const canConfirm = reason.trim() !== '' && !pending

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

        <div className="space-y-1.5">
          <Label htmlFor="config-reason-input">{t('cluster.servers.reason.label')}</Label>
          <Textarea
            id="config-reason-input"
            aria-label={t('cluster.servers.reason.label')}
            value={reason}
            onChange={(e) => {
              setReason(e.target.value)
            }}
            placeholder={t('cluster.servers.reason.placeholder')}
            rows={2}
          />
        </div>

        {errorText && <p className="text-sm text-destructive">{errorText}</p>}

        <AlertDialogFooter>
          <AlertDialogCancel>{t('delivery.configs.create.cancel')}</AlertDialogCancel>
          <AlertDialogAction
            disabled={!canConfirm}
            onClick={(e) => {
              // 阻止 radix 默认关闭，交由调用方在成功后关闭
              e.preventDefault()
              onConfirm(reason.trim())
            }}
          >
            {confirmLabel}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
