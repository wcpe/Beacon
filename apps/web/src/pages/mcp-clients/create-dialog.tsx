// 申请创建 MCP 客户端弹窗：名称 + profile + 审批原因。
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
  Textarea,
} from '@beacon/ui'
import type { CreateMCPClientBody, MCPClientProfile } from '@beacon/contracts'

interface CreateDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  pending: boolean
  errorText: string | null
  onSubmit: (body: CreateMCPClientBody) => void
}

export default function CreateDialog({ open, onOpenChange, pending, errorText, onSubmit }: CreateDialogProps) {
  const { t } = useTranslation()
  const [displayName, setDisplayName] = useState('')
  const [profile, setProfile] = useState<MCPClientProfile>('observer')
  const [reason, setReason] = useState('')

  // 每次打开清空草稿
  useEffect(() => {
    if (open) {
      setDisplayName('')
      setProfile('observer')
      setReason('')
    }
  }, [open])

  const canSubmit = displayName.trim() !== '' && reason.trim() !== '' && !pending

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('system.mcpClients.createTitle')}</DialogTitle>
          <DialogDescription>{t('system.mcpClients.mission')}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          <div className="grid gap-1.5">
            <Label htmlFor="mcp-client-name">{t('system.mcpClients.displayNameLabel')}</Label>
            <Input
              id="mcp-client-name"
              value={displayName}
              onChange={(e) => {
                setDisplayName(e.target.value)
              }}
              placeholder={t('system.mcpClients.displayNamePlaceholder')}
            />
          </div>
          <div className="grid gap-1.5">
            <Label>{t('system.mcpClients.profileLabel')}</Label>
            <Select
              value={profile}
              onValueChange={(value) => {
                setProfile(value as MCPClientProfile)
              }}
            >
              <SelectTrigger aria-label={t('system.mcpClients.profileLabel')}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="observer">{t('system.mcpClients.profile.observer')}</SelectItem>
                <SelectItem value="automation">{t('system.mcpClients.profile.automation')}</SelectItem>
              </SelectContent>
            </Select>
            {/* 选中 profile 的能力边界就地说明，避免误授权限 */}
            <p className="text-xs text-muted-foreground">{t(`system.mcpClients.profileHint.${profile}`)}</p>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="mcp-client-reason">{t('system.mcpClients.reasonLabel')}</Label>
            <Textarea
              id="mcp-client-reason"
              value={reason}
              onChange={(e) => {
                setReason(e.target.value)
              }}
              placeholder={t('system.mcpClients.reasonPlaceholder')}
              rows={2}
            />
          </div>
          {errorText && <p className="text-sm text-destructive">{errorText}</p>}
        </div>
        <DialogFooter>
          <Button
            disabled={!canSubmit}
            onClick={() => {
              onSubmit({ displayName: displayName.trim(), profile, reason: reason.trim() })
            }}
          >
            {pending ? t('system.mcpClients.creating') : t('system.mcpClients.createConfirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
