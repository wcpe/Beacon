// env 创建 / 编辑弹窗（FR-178）：名称 + 描述。创建 / 改名撞名由后端 409 返回，经 errorText 内联展示。
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

interface EnvFormDialogProps {
  open: boolean
  mode: 'create' | 'edit'
  initialCode: string
  initialDisplayName: string
  initialDescription: string
  pending: boolean
  errorText: string | null
  onOpenChange: (open: boolean) => void
  onSubmit: (code: string, displayName: string, description: string) => void
}

export default function EnvFormDialog({
  open,
  mode,
  initialCode,
  initialDisplayName,
  initialDescription,
  pending,
  errorText,
  onOpenChange,
  onSubmit,
}: EnvFormDialogProps) {
  const { t } = useTranslation()
  const [code, setCode] = useState(initialCode)
  const [displayName, setDisplayName] = useState(initialDisplayName)
  const [description, setDescription] = useState(initialDescription)

  // 每次打开时用传入初值重置表单（创建为空、编辑为当前值）
  useEffect(() => {
    if (open) {
      setCode(initialCode)
      setDisplayName(initialDisplayName)
      setDescription(initialDescription)
    }
  }, [open, initialCode, initialDisplayName, initialDescription])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{mode === 'create' ? t('system.envs.createTitle') : t('system.envs.editTitle')}</DialogTitle>
          <DialogDescription>{t('system.envs.hint')}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          <div className="grid gap-1.5">
            <Label htmlFor="env-code">{t('system.envs.codeLabel')}</Label>
            <Input
              id="env-code"
              value={code}
              onChange={(e) => {
                setCode(e.target.value)
              }}
              placeholder={t('system.envs.codePlaceholder')}
              readOnly={mode === 'edit'}
            />
            <p className="text-xs text-ink-4">{t('system.envs.codeHint')}</p>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="env-display-name">{t('system.envs.displayNameLabel')}</Label>
            <Input
              id="env-display-name"
              value={displayName}
              onChange={(e) => {
                setDisplayName(e.target.value)
              }}
              placeholder={t('system.envs.displayNamePlaceholder')}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="env-desc">{t('system.envs.descLabel')}</Label>
            <Textarea
              id="env-desc"
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
            disabled={code.trim() === '' || displayName.trim() === '' || pending}
            onClick={() => {
              onSubmit(code.trim(), displayName.trim(), description.trim())
            }}
          >
            {pending ? t('system.envs.saving') : t('system.envs.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
