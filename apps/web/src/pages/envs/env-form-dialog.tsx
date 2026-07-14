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
  initialName: string
  initialDescription: string
  pending: boolean
  errorText: string | null
  onOpenChange: (open: boolean) => void
  onSubmit: (name: string, description: string) => void
}

export default function EnvFormDialog({
  open,
  mode,
  initialName,
  initialDescription,
  pending,
  errorText,
  onOpenChange,
  onSubmit,
}: EnvFormDialogProps) {
  const { t } = useTranslation()
  const [name, setName] = useState(initialName)
  const [description, setDescription] = useState(initialDescription)

  // 每次打开时用传入初值重置表单（创建为空、编辑为当前值）
  useEffect(() => {
    if (open) {
      setName(initialName)
      setDescription(initialDescription)
    }
  }, [open, initialName, initialDescription])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{mode === 'create' ? t('system.envs.createTitle') : t('system.envs.editTitle')}</DialogTitle>
          <DialogDescription>{t('system.envs.hint')}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          <div className="grid gap-1.5">
            <Label htmlFor="env-name">{t('system.envs.nameLabel')}</Label>
            <Input
              id="env-name"
              value={name}
              onChange={(e) => {
                setName(e.target.value)
              }}
              placeholder={t('system.envs.namePlaceholder')}
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
            disabled={name.trim() === '' || pending}
            onClick={() => {
              onSubmit(name.trim(), description.trim())
            }}
          >
            {pending ? t('system.envs.saving') : t('system.envs.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
