// 创建 API 密钥弹窗：名称 + 角色（读写 / 只读）+ 过期时间（可选）。
// 提交进行中禁用，内联脱敏错误展示（ADR-0057）。
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
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@beacon/ui'

import type { CreateApiKeyBody } from '../../api/system'

interface CreateDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  pending: boolean
  errorText: string | null
  onSubmit: (body: CreateApiKeyBody) => void
}

export default function CreateDialog({ open, onOpenChange, pending, errorText, onSubmit }: CreateDialogProps) {
  const { t } = useTranslation()
  const [name, setName] = useState('')
  const [role, setRole] = useState<'full' | 'readonly'>('readonly')
  const [expiresAt, setExpiresAt] = useState('')

  // 每次打开清空草稿
  useEffect(() => {
    if (open) {
      setName('')
      setRole('readonly')
      setExpiresAt('')
    }
  }, [open])

  const canSubmit = name.trim() !== '' && !pending

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('system.apiKeys.createTitle')}</DialogTitle>
          <DialogDescription>{t('system.apiKeys.mission')}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          <div className="grid gap-1.5">
            <Label htmlFor="api-key-name">{t('system.apiKeys.nameLabel')}</Label>
            <Input
              id="api-key-name"
              value={name}
              onChange={(e) => {
                setName(e.target.value)
              }}
              placeholder={t('system.apiKeys.namePlaceholder')}
            />
          </div>
          <div className="grid gap-1.5">
            <Label>{t('system.apiKeys.roleLabel')}</Label>
            <Select
              value={role}
              onValueChange={(value) => {
                setRole(value as 'full' | 'readonly')
              }}
            >
              <SelectTrigger aria-label={t('system.apiKeys.roleLabel')}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="readonly">{t('system.apiKeys.role.readonly')}</SelectItem>
                <SelectItem value="full">{t('system.apiKeys.role.full')}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="api-key-expires">{t('system.apiKeys.expiresLabel')}</Label>
            <Input
              id="api-key-expires"
              type="date"
              value={expiresAt}
              onChange={(e) => {
                setExpiresAt(e.target.value)
              }}
            />
            <p className="text-xs text-muted-foreground">{t('system.apiKeys.expiresHint')}</p>
          </div>
          {errorText && <p className="text-sm text-destructive">{errorText}</p>}
        </div>
        <DialogFooter>
          <Button
            disabled={!canSubmit}
            onClick={() => {
              onSubmit({
                name: name.trim(),
                role,
                expiresAt: expiresAt === '' ? undefined : new Date(expiresAt).toISOString(),
              })
            }}
          >
            {pending ? t('system.apiKeys.creating') : t('system.apiKeys.createConfirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
