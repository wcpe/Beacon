// 新建结构节点弹窗（集群 / 大区 / 小区共用）：名称 + 描述，提交带内联错误展示。
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
} from '@beacon/ui'

interface CreateNodeDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  pending: boolean
  errorText?: string | null
  onSubmit: (name: string, description: string) => void
}

export default function CreateNodeDialog({
  open,
  onOpenChange,
  title,
  pending,
  errorText,
  onSubmit,
}: CreateNodeDialogProps) {
  const { t } = useTranslation()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')

  // 每次打开清空草稿
  useEffect(() => {
    if (open) {
      setName('')
      setDescription('')
    }
  }, [open])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="space-y-1.5">
            <Label htmlFor="create-node-name">{t('cluster.zones.create.name')}</Label>
            <Input
              id="create-node-name"
              value={name}
              onChange={(e) => {
                setName(e.target.value)
              }}
              placeholder={t('cluster.zones.create.namePlaceholder')}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="create-node-desc">{t('cluster.zones.create.description')}</Label>
            <Input
              id="create-node-desc"
              value={description}
              onChange={(e) => {
                setDescription(e.target.value)
              }}
            />
          </div>
          {errorText && <p className="text-sm text-crit">{errorText}</p>}
        </div>
        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => {
              onOpenChange(false)
            }}
          >
            {t('cluster.zones.create.cancel')}
          </Button>
          <Button
            disabled={name.trim() === '' || pending}
            onClick={() => {
              onSubmit(name.trim(), description.trim())
            }}
          >
            {t('cluster.zones.create.submit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
