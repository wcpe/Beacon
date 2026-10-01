// namespace 编辑弹窗（FR-239）：只可修改展示名（displayName）与描述（description）。
// `code` 是稳定业务标识，创建后不可变更（见 docs/specs/stable-business-identifiers-and-display-names.md）：
// 因此这里以「只读输入 + 不可变提示」呈现，既不出现在提交体里，也不给任何编辑控件；
// 即便绕过 UI 强行提交，服务端仍会以 400 IMMUTABLE_IDENTIFIER 拒绝。
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
  Input,
  Label,
  Textarea,
} from '@beacon/ui'

interface NamespaceEditDialogProps {
  open: boolean
  // 稳定业务标识（只读展示，不参与提交）
  initialCode: string
  // 当前展示名 / 描述的初值
  initialDisplayName: string
  initialDescription: string
  pending: boolean
  errorText: string | null
  onOpenChange: (open: boolean) => void
  onSubmit: (displayName: string, description: string) => void
}

export default function NamespaceEditDialog({
  open,
  initialCode,
  initialDisplayName,
  initialDescription,
  pending,
  errorText,
  onOpenChange,
  onSubmit,
}: NamespaceEditDialogProps) {
  const { t } = useTranslation()
  const [displayName, setDisplayName] = useState(initialDisplayName)
  const [description, setDescription] = useState(initialDescription)

  // 每次打开都按当前值重置表单，避免上一次未提交的编辑内容残留
  useEffect(() => {
    if (open) {
      setDisplayName(initialDisplayName)
      setDescription(initialDescription)
    }
  }, [open, initialDisplayName, initialDescription])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('system.namespaces.editTitle')}</DialogTitle>
          <DialogDescription>{t('system.namespaces.editDesc')}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          {/* 稳定业务标识：只读呈现 + 显式说明不可变 */}
          <div className="grid gap-1.5">
            <Label htmlFor="namespace-edit-code">{t('system.namespaces.codeLabel')}</Label>
            <Input id="namespace-edit-code" value={initialCode} readOnly aria-readonly className="bg-surface-2 text-ink-3" />
            <p className="text-xs text-ink-4">{t('system.namespaces.codeImmutableHint')}</p>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="namespace-edit-display-name">{t('system.namespaces.displayNameLabel')}</Label>
            <Input
              id="namespace-edit-display-name"
              value={displayName}
              onChange={(e) => {
                setDisplayName(e.target.value)
              }}
              placeholder={t('system.namespaces.displayNamePlaceholder')}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="namespace-edit-desc">{t('system.namespaces.descLabel')}</Label>
            <Textarea
              id="namespace-edit-desc"
              value={description}
              onChange={(e) => {
                setDescription(e.target.value)
              }}
              rows={2}
            />
          </div>
          {errorText && <p className="text-sm text-destructive">{errorText}</p>}
        </div>
        <DialogFooter>
          <Button
            disabled={displayName.trim() === '' || pending}
            onClick={() => {
              onSubmit(displayName.trim(), description.trim())
            }}
          >
            {pending ? t('system.namespaces.saving') : t('system.namespaces.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
