// 改派对话框（FR-71，高摩擦显式改派）：取代原拖拽归派。
// 选目标大区/小区（Combobox，候选与指派表单同源）+ 手输目标 serverId 原样复述（像 GitHub 删库手打仓库名），
// 输入须 === 该卡 serverId 且选齐目标区才启用「确认改派」。提交以入参回调 onConfirm（页面调既有 assignZone）。
// 纯前端摩擦（防误触），后端排空门才是安全边界（ADR-0036）。

import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { InstanceView } from '../../api/types'
import type { AssignParams } from '../../api/client'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Combobox } from '@/components/ui/combobox'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

interface ReassignDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  // 被改派的实例（null 时不渲染内容）；serverId 用于手输复述比对
  instance: InstanceView | null
  // 该实例现有指派备注（沿用，避免改派清空运维填写的备注；可被对话框内编辑覆盖）
  currentNote: string
  // 目标大区 / 小区候选（与指派表单同源：zone 汇总与实例并集）
  groupOptions: string[]
  zoneOptions: string[]
  // 提交进行中（禁用确认按钮）
  pending: boolean
  // 确认改派回调：以完整入参交由页面调既有 assignZone
  onConfirm: (params: AssignParams) => void
}

export default function ReassignDialog({
  open,
  onOpenChange,
  instance,
  currentNote,
  groupOptions,
  zoneOptions,
  pending,
  onConfirm,
}: ReassignDialogProps) {
  const { t } = useTranslation()
  // 目标区与手输复述的草稿；备注初值取现有指派备注
  const [group, setGroup] = useState('')
  const [zone, setZone] = useState('')
  const [typed, setTyped] = useState('')
  const [note, setNote] = useState('')

  // 每次打开（或换目标实例）时重置草稿：目标区留空强制显式选择（高摩擦防误触），
  // 备注取现有指派备注（沿用，避免改派清空运维填写的备注），手输复述清空。
  useEffect(() => {
    if (open && instance) {
      setGroup('')
      setZone('')
      setTyped('')
      setNote(currentNote)
    }
  }, [open, instance, currentNote])

  if (!instance) return null

  // 手输复述须严格等于该卡 serverId（防误触）；目标大区 / 小区须选齐
  const typedMatches = typed === instance.serverId
  const canConfirm = typedMatches && group !== '' && zone !== '' && !pending

  function handleConfirm() {
    if (!instance || !canConfirm) return
    onConfirm({
      namespace: instance.namespace,
      serverId: instance.serverId,
      group,
      zone,
      note: note.trim(),
    })
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t('zones.reassignTitle', { serverId: instance.serverId })}</DialogTitle>
          <DialogDescription>{t('zones.reassignDesc')}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          <div className="space-y-1.5">
            <Label htmlFor="r-group">{t('zones.reassignTargetGroup')}</Label>
            {/* 严格选：目标须为已存在维度（候选来自 zone 汇总与实例并集） */}
            <Combobox
              id="r-group"
              aria-label={t('common.group')}
              value={group}
              onChange={setGroup}
              options={groupOptions}
              allowCustom={false}
              placeholder={t('common.pleaseSelect')}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="r-zone">{t('zones.reassignTargetZone')}</Label>
            <Combobox
              id="r-zone"
              aria-label={t('common.zone')}
              value={zone}
              onChange={setZone}
              options={zoneOptions}
              allowCustom={false}
              placeholder={t('common.pleaseSelect')}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="r-typed">{t('zones.reassignTypeLabel')}</Label>
            <Input
              id="r-typed"
              aria-label={t('zones.reassignTypeLabel')}
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              placeholder={instance.serverId}
              autoComplete="off"
              className="font-mono"
            />
            {!typedMatches && (
              <p className="text-xs text-muted-foreground">
                {t('zones.reassignTypeHint', { serverId: instance.serverId })}
              </p>
            )}
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="r-note">{t('zones.formNote')}</Label>
            <Input id="r-note" value={note} onChange={(e) => setNote(e.target.value)} />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel')}
          </Button>
          <Button onClick={handleConfirm} disabled={!canConfirm}>
            {t('zones.reassignConfirmBtn')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
