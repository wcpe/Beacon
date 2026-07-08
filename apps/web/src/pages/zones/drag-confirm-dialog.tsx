// 拖拽落区 / 改派的二次确认弹窗：
// ① 首次分配（未分配服务器拖到兼容目标）——纯确认，无需原因；
// ② 已分配服务器改派（换区 / 改集群）——走换区工单，需填原因才放行。
// 松手后不立即写，先弹此确认，确认才调对应 mutation（首次分配 / 换区）。

import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  Label,
  Textarea,
} from '@beacon/ui'

// 待确认的拖拽落区意图：首次分配或改派
export interface PendingDrop {
  // assign 首次分配（无原因）；rezone 已分配改派（需原因）
  mode: 'assign' | 'rezone'
  serverRowId: number
  serverId: string
  // 目标：小区（backend）或 BC 集群（proxy）
  target: { kind: 'zone' | 'bc_cluster'; id: number }
  targetName: string
  // 改派时的原归属可读名（rezone 用）
  fromName?: string
}

interface DragConfirmDialogProps {
  pending: PendingDrop | null
  onOpenChange: (open: boolean) => void
  submitting: boolean
  errorText?: string | null
  // 确认回调：rezone 传回原因，assign 传空串
  onConfirm: (reason: string) => void
}

export default function DragConfirmDialog({
  pending,
  onOpenChange,
  submitting,
  errorText,
  onConfirm,
}: DragConfirmDialogProps) {
  const { t } = useTranslation()
  const [reason, setReason] = useState('')

  // 每次打开清空原因草稿
  useEffect(() => {
    if (pending) {
      setReason('')
    }
  }, [pending])

  const isRezone = pending?.mode === 'rezone'
  // 改派须填原因；首次分配恒可确认
  const canConfirm = !submitting && (!isRezone || reason.trim() !== '')

  const title = isRezone
    ? t('cluster.zones.drag.confirmRezoneTitle')
    : t('cluster.zones.drag.confirmAssignTitle')
  const description = pending
    ? isRezone
      ? t('cluster.zones.drag.confirmRezoneDesc', {
          serverId: pending.serverId,
          from: pending.fromName ?? '-',
          target: pending.targetName,
        })
      : t('cluster.zones.drag.confirmAssignDesc', { serverId: pending.serverId, target: pending.targetName })
    : ''

  return (
    <AlertDialog open={pending !== null} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>

        {/* 改派需填原因（换区工单要求） */}
        {isRezone && (
          <div className="space-y-1.5">
            <Label htmlFor="rezone-reason">{t('cluster.zones.drag.rezoneReasonLabel')}</Label>
            <Textarea
              id="rezone-reason"
              value={reason}
              onChange={(e) => {
                setReason(e.target.value)
              }}
              placeholder={t('cluster.zones.drag.rezoneReasonPlaceholder')}
              rows={3}
            />
          </div>
        )}

        {errorText != null && errorText !== '' && <p className="text-sm text-crit">{errorText}</p>}

        <AlertDialogFooter>
          <AlertDialogCancel>{t('cluster.zones.drag.cancel')}</AlertDialogCancel>
          <AlertDialogAction
            disabled={!canConfirm}
            onClick={() => {
              onConfirm(reason.trim())
            }}
          >
            {t('cluster.zones.drag.confirm')}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
