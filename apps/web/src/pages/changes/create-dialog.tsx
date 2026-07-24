// 新建变更单弹窗：标题（必填）+ 描述 + 模板源子服（可选）。提交创建 draft 单，内联脱敏错误。
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Button,
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Input,
  Label,
  Textarea,
} from '@beacon/ui'

export interface CreateDraftInput {
  title: string
  description: string
  sourceServerId: string | null
}

interface CreateDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  pending: boolean
  errorText?: string | null
  onSubmit: (input: CreateDraftInput) => void
}

export default function CreateDialog({
  open,
  onOpenChange,
  pending,
  errorText,
  onSubmit,
}: CreateDialogProps) {
  const { t } = useTranslation()
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [source, setSource] = useState('')

  // 每次打开清空草稿
  useEffect(() => {
    if (open) {
      setTitle('')
      setDescription('')
      setSource('')
    }
  }, [open])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('delivery.changes.create.title')}</DialogTitle>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="space-y-1.5">
            <Label htmlFor="change-create-title">{t('delivery.changes.create.titleLabel')}</Label>
            <Input
              id="change-create-title"
              value={title}
              onChange={(e) => {
                setTitle(e.target.value)
              }}
              placeholder={t('delivery.changes.create.titlePlaceholder')}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="change-create-desc">{t('delivery.changes.create.descLabel')}</Label>
            <Textarea
              id="change-create-desc"
              value={description}
              onChange={(e) => {
                setDescription(e.target.value)
              }}
              rows={2}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="change-create-source">{t('delivery.changes.create.sourceLabel')}</Label>
            <Input
              id="change-create-source"
              value={source}
              onChange={(e) => {
                setSource(e.target.value)
              }}
              placeholder={t('delivery.changes.create.sourcePlaceholder')}
            />
          </div>
          {errorText !== null && errorText !== undefined && errorText !== '' && (
            <p className="text-sm text-destructive">{errorText}</p>
          )}
        </div>
        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => {
              onOpenChange(false)
            }}
          >
            {t('delivery.changes.create.cancel')}
          </Button>
          <Button
            disabled={title.trim() === '' || pending}
            onClick={() => {
              onSubmit({
                title: title.trim(),
                description: description.trim(),
                sourceServerId: source.trim() === '' ? null : source.trim(),
              })
            }}
          >
            {t('delivery.changes.create.confirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
