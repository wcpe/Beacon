// 授予单向信任弹窗：来源 / 目标 namespace + 能力 + 原因必填。
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
  Label,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Textarea,
} from '@beacon/ui'
import type { NamespaceItem } from '@beacon/devmock'

import type { GrantTrustBody } from '../../api/system'

type Capability = GrantTrustBody['capability']

interface GrantDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  namespaces: NamespaceItem[]
  pending: boolean
  errorText: string | null
  onSubmit: (body: GrantTrustBody) => void
}

const CAPABILITIES: Capability[] = ['schedule', 'message', 'agent_ops']

export default function GrantDialog({
  open,
  onOpenChange,
  namespaces,
  pending,
  errorText,
  onSubmit,
}: GrantDialogProps) {
  const { t } = useTranslation()
  const [fromId, setFromId] = useState('')
  const [toId, setToId] = useState('')
  const [capability, setCapability] = useState<Capability>('schedule')
  const [note, setNote] = useState('')

  useEffect(() => {
    if (open) {
      setFromId('')
      setToId('')
      setCapability('schedule')
      setNote('')
    }
  }, [open])

  // 目标不能与来源相同、原因必填
  const canSubmit = fromId !== '' && toId !== '' && fromId !== toId && note.trim() !== '' && !pending

  const capabilityLabel = (cap: Capability): string => {
    if (cap === 'schedule') {
      return t('system.namespaces.trusts.capabilitySchedule')
    }
    if (cap === 'message') {
      return t('system.namespaces.trusts.capabilityMessage')
    }
    return t('system.namespaces.trusts.capabilityAgentOps')
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('system.namespaces.trusts.grantTitle')}</DialogTitle>
          <DialogDescription>{t('system.namespaces.trusts.desc')}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          <div className="grid gap-1.5">
            <Label>{t('system.namespaces.trusts.fromLabel')}</Label>
            <Select value={fromId} onValueChange={setFromId}>
              <SelectTrigger aria-label={t('system.namespaces.trusts.fromLabel')}>
                <SelectValue placeholder={t('system.namespaces.trusts.selectNamespace')} />
              </SelectTrigger>
              <SelectContent>
                {namespaces.map((ns) => (
                  <SelectItem key={ns.id} value={String(ns.id)}>
                    {ns.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="grid gap-1.5">
            <Label>{t('system.namespaces.trusts.toLabel')}</Label>
            <Select value={toId} onValueChange={setToId}>
              <SelectTrigger aria-label={t('system.namespaces.trusts.toLabel')}>
                <SelectValue placeholder={t('system.namespaces.trusts.selectNamespace')} />
              </SelectTrigger>
              <SelectContent>
                {namespaces.map((ns) => (
                  <SelectItem key={ns.id} value={String(ns.id)}>
                    {ns.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="grid gap-1.5">
            <Label>{t('system.namespaces.trusts.capabilityLabel')}</Label>
            <Select
              value={capability}
              onValueChange={(value) => {
                setCapability(value as Capability)
              }}
            >
              <SelectTrigger aria-label={t('system.namespaces.trusts.capabilityLabel')}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {CAPABILITIES.map((cap) => (
                  <SelectItem key={cap} value={cap}>
                    {capabilityLabel(cap)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="trust-note">{t('system.namespaces.trusts.noteLabel')}</Label>
            <Textarea
              id="trust-note"
              value={note}
              onChange={(e) => {
                setNote(e.target.value)
              }}
              placeholder={t('system.namespaces.trusts.notePlaceholder')}
              rows={2}
            />
          </div>
          {errorText && <p className="text-sm text-destructive">{errorText}</p>}
        </div>
        <DialogFooter>
          <Button
            disabled={!canSubmit}
            onClick={() => {
              onSubmit({
                fromNamespaceId: Number.parseInt(fromId, 10),
                toNamespaceId: Number.parseInt(toId, 10),
                capability,
                note: note.trim(),
              })
            }}
          >
            {pending ? t('system.namespaces.trusts.granting') : t('system.namespaces.trusts.grantConfirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
