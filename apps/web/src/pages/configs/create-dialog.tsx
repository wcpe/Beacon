// 新建配置文件弹窗：文件名 + 格式（yaml/json/properties）+ 描述，提交带内联脱敏错误（重名 409）。
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
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@beacon/ui'
import type { ConfigFormat } from '@beacon/devmock'

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

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('delivery.configs.create.title')}</DialogTitle>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="space-y-1.5">
            <Label htmlFor="config-create-name">{t('delivery.configs.create.nameLabel')}</Label>
            <Input
              id="config-create-name"
              value={name}
              onChange={(e) => {
                setName(e.target.value)
              }}
              placeholder={t('delivery.configs.create.namePlaceholder')}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="config-create-format">{t('delivery.configs.create.formatLabel')}</Label>
            <Select
              value={format}
              onValueChange={(value) => {
                setFormat(value as ConfigFormat)
              }}
            >
              <SelectTrigger
                id="config-create-format"
                className="w-40"
                aria-label={t('delivery.configs.create.formatLabel')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {FORMATS.map((f) => (
                  <SelectItem key={f} value={f}>
                    {f}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
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
          {errorText && <p className="text-sm text-destructive">{errorText}</p>}
        </div>
        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => {
              onOpenChange(false)
            }}
          >
            {t('delivery.configs.create.cancel')}
          </Button>
          <Button
            disabled={name.trim() === '' || pending}
            onClick={() => {
              onSubmit(name.trim(), format, description.trim())
            }}
          >
            {t('delivery.configs.create.confirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
