// 系统域带原因输入的写操作确认弹窗：承 UX §4「高风险二次确认 + 原因输入」。
// 影响预览 + 可选原因必填 + 提交进行中禁用 + 内联脱敏错误展示（ADR-0057，不静默隐藏）。
// 与集群域 servers/reason-dialog 同构，供系统域各页复用（回滚 / 授予 / 收回 / 归档确认）。

import { useEffect, useState, type ReactNode } from 'react'

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

interface SystemReasonDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description: string
  confirmLabel: string
  // 影响预览行
  impacts?: string[]
  // 是否要求填写原因（默认 true）
  requireReason?: boolean
  // 原因输入标签
  reasonLabel: string
  // 原因输入占位
  reasonPlaceholder?: string
  // 取消按钮文案
  cancelLabel: string
  pending: boolean
  // 展示的脱敏错误文案（提交失败时）
  errorText?: string | null
  // 额外内容，渲染在原因输入之上
  children?: ReactNode
  // 确认回调：把原因回传给调用方触发写操作
  onConfirm: (reason: string) => void
}

export default function SystemReasonDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  impacts,
  requireReason = true,
  reasonLabel,
  reasonPlaceholder,
  cancelLabel,
  pending,
  errorText,
  children,
  onConfirm,
}: SystemReasonDialogProps) {
  const [reason, setReason] = useState('')

  // 每次打开清空原因草稿，避免残留上次输入
  useEffect(() => {
    if (open) {
      setReason('')
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

        {children}

        {requireReason && (
          <div className="space-y-1.5">
            <Label htmlFor="system-reason-input">{reasonLabel}</Label>
            <Textarea
              id="system-reason-input"
              aria-label={reasonLabel}
              value={reason}
              onChange={(e) => {
                setReason(e.target.value)
              }}
              placeholder={reasonPlaceholder ?? ''}
              rows={2}
            />
          </div>
        )}

        {errorText && <p className="text-sm text-destructive">{errorText}</p>}

        <AlertDialogFooter>
          <AlertDialogCancel>{cancelLabel}</AlertDialogCancel>
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
