// 新建配置文件弹窗：文件名 + 格式（yaml/json/properties）+ 描述，提交带内联脱敏错误（重名 409）。
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Button,
  Combobox,
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Input,
  Label,
} from '@beacon/ui'
import type { ConfigFormat } from '@beacon/contracts'

const FORMATS: readonly ConfigFormat[] = ['yaml', 'json', 'properties']

interface CreateDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  pending: boolean
  errorText?: string | null
  onSubmit: (name: string, format: ConfigFormat, description: string) => void
}

export default function CreateDialog({
  open,
  onOpenChange,
  pending,
  errorText,
  onSubmit,
}: CreateDialogProps) {
  const { t } = useTranslation()
  const [name, setName] = useState('')
  const [format, setFormat] = useState<ConfigFormat>('yaml')
  const [description, setDescription] = useState('')

  // 每次打开清空草稿
  useEffect(() => {
    if (open) {
      setName('')
      setFormat('yaml')
      setDescription('')
    }
  }, [open])

  const canSubmit = name.trim() !== '' && !pending
  const submit = (): void => {
    if (canSubmit) {
      onSubmit(name.trim(), format, description.trim())
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
          <DialogTitle className="text-ink-1">{t('delivery.configs.create.title')}</DialogTitle>
        </DialogHeader>
        <div className="min-h-0 grid gap-3 overflow-y-auto pr-1">
          <div className="space-y-1.5">
            <Label htmlFor="config-create-name">{t('delivery.configs.create.nameLabel')}</Label>
            <Input
              id="config-create-name"
              value={name}
              onKeyDown={(event) => {
                if (event.key === 'Enter') {
                  event.preventDefault()
                  submit()
                }
              }}
              onChange={(e) => {
                setName(e.target.value)
              }}
              placeholder={t('delivery.configs.create.namePlaceholder')}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="config-create-format">{t('delivery.configs.create.formatLabel')}</Label>
            <Combobox
              id="config-create-format"
              className="w-40"
              aria-label={t('delivery.configs.create.formatLabel')}
              value={format}
              onChange={(value) => {
                setFormat(value as ConfigFormat)
              }}
              options={[...FORMATS]}
              allowCustom={false}
              placeholder={t('delivery.configs.create.formatLabel')}
              emptyText={t('common.combobox.noMatch')}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="config-create-desc">{t('delivery.configs.create.descLabel')}</Label>
            <Input
              id="config-create-desc"
              value={description}
              onChange={(e) => {
                setDescription(e.target.value)
              }}
            />
          </div>
          {errorText && <p role="alert" className="text-sm text-crit">{errorText}</p>}
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
            {t('delivery.configs.create.cancel')}
          </Button>
          <Button type="button" disabled={!canSubmit} onClick={submit}>
            {t('delivery.configs.create.confirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
