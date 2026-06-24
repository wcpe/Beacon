// 通用破坏性二次确认对话框（FR-76，承 FR-67 确认范式）：
// 把散落各页的删除 / 吊销等破坏性写操作的二次确认收敛为一处——
// 标题 + 破坏性动作描述 + 影响摘要（脱链哪层 / 影响哪些服，调用方传入）+ 确认 / 取消。
// 可选「需输入复述」高摩擦档（参考 FR-71 改派手输 serverId）：传 confirmPhrase 时
// 须手输 === confirmPhrase 才启用确认按钮，防误触。纯前端摩擦，后端守卫才是安全边界。

import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'

interface DestructiveConfirmDialogProps {
  // 受控开合
  open: boolean
  onOpenChange: (open: boolean) => void
  // 标题（如「删除环境「生产」？」）
  title: string
  // 破坏性动作描述（不可恢复 / 立即失效等）
  description: string
  // 确认按钮文案（如「确认删除」「确认吊销」）
  confirmLabel: string
  // 影响摘要：脱链哪层 / 影响哪些服，由调用方按上下文传入；空数组则不渲染该区
  impacts?: string[]
  // 高摩擦档：非空时渲染手输框，输入须 === 本值才启用确认；为空则确认按钮常态可点
  confirmPhrase?: string
  // 提交进行中（禁用确认按钮）
  pending?: boolean
  // 确认回调：交由调用页触发既有写操作
  onConfirm: () => void
}

export default function DestructiveConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  impacts,
  confirmPhrase,
  pending = false,
  onConfirm,
}: DestructiveConfirmDialogProps) {
  const { t } = useTranslation()
  // 手输复述草稿：每次打开时清空，避免残留上次输入
  const [typed, setTyped] = useState('')

  useEffect(() => {
    if (open) setTyped('')
  }, [open])

  // 高摩擦档须手输与 confirmPhrase 相符（首尾空白不计，避免名称带尾空格时照抄也无法确认而卡死）；
  // 仍区分大小写以保留摩擦强度。无 confirmPhrase 时该闸恒通过。
  const phraseMatches = !confirmPhrase || typed.trim() === confirmPhrase.trim()
  const canConfirm = phraseMatches && !pending

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>

        {/* 影响摘要：脱链哪层 / 影响哪些服 */}
        {impacts && impacts.length > 0 && (
          <ul className="list-disc space-y-1 rounded-md bg-muted/50 px-5 py-3 text-sm text-muted-foreground">
            {impacts.map((line, i) => (
              <li key={i}>{line}</li>
            ))}
          </ul>
        )}

        {/* 高摩擦档：手输复述指定短语才放行 */}
        {confirmPhrase && (
          <div className="space-y-1.5">
            <Label htmlFor="destructive-confirm-phrase">
              {t('common.destructiveTypeLabel', { phrase: confirmPhrase })}
            </Label>
            <Input
              id="destructive-confirm-phrase"
              aria-label={t('common.destructiveTypeAria')}
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              placeholder={confirmPhrase}
              autoComplete="off"
              className="font-mono"
            />
          </div>
        )}

        <AlertDialogFooter>
          <AlertDialogCancel>{t('common.cancel')}</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={!canConfirm}
            onClick={onConfirm}
          >
            {confirmLabel}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
