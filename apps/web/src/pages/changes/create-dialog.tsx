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

import ServerPicker from '../../features/delivery/server-picker'

export interface CreateDraftInput {
  title: string
  description: string
  sourceServerId: string | null
}

interface CreateDialogProps {
  // 模板源候选取自当前命名空间（与引导向导的候选口径一致）
  namespaceId: number
  open: boolean
  onOpenChange: (open: boolean) => void
  pending: boolean
  errorText?: string | null
  onSubmit: (input: CreateDraftInput) => void
}

export default function CreateDialog({
  namespaceId,
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

  const canSubmit = title.trim() !== '' && !pending
  const submit = (): void => {
    if (canSubmit) {
      onSubmit({
        title: title.trim(),
        description: description.trim(),
        sourceServerId: source.trim() === '' ? null : source.trim(),
      })
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!pending) {
          onOpenChange(next)
        }
      }}
    >
      <DialogContent
        className="grid-rows-[auto_minmax(0,1fr)_auto] max-h-[calc(100dvh-2rem)] overflow-hidden"
        showCloseButton={!pending}
        onInteractOutside={(event) => {
          event.preventDefault()
        }}
      >
        <DialogHeader>
          <DialogTitle>{t('delivery.changes.create.title')}</DialogTitle>
        </DialogHeader>
        <div className="min-h-0 grid gap-3 overflow-y-auto pr-1">
          <div className="space-y-1.5">
            <Label htmlFor="change-create-title">{t('delivery.changes.create.titleLabel')}</Label>
            <Input
              id="change-create-title"
              value={title}
              onKeyDown={(event) => {
                if (event.key === 'Enter') {
                  event.preventDefault()
                  submit()
                }
              }}
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
            {/* 模板源必须是在线且已分配的后端服：系统要读它的实际文件做差异扫描，手填拼错会静默扫不出差异 */}
            <ServerPicker
              id="change-create-source"
              aria-label={t('delivery.changes.create.sourceLabel')}
              namespaceId={namespaceId}
              value={source}
              onChange={setSource}
              assigned
              onlineOnly
              clearable
              placeholder={t('delivery.changes.create.sourcePlaceholder')}
            />
          </div>
          {errorText !== null && errorText !== undefined && errorText !== '' && (
            <p role="alert" className="text-sm text-destructive">{errorText}</p>
          )}
        </div>
        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            disabled={pending}
            onClick={() => {
              onOpenChange(false)
            }}
          >
            {t('delivery.changes.create.cancel')}
          </Button>
          <Button type="button" disabled={!canSubmit} onClick={submit}>
            {t('delivery.changes.create.confirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
