// 已分配服务器改派（换区 / 改集群）弹窗：右键菜单「改派到…」触发。
// 选新目标（可搜索树，复用 AssignTargetTree）→ 填原因（换区工单必填）→ 确认调 server-rezones。
// 与拖拽改派共用后端换区端点，只是入口不同（点选 vs 拖放）。

import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Button,
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Label,
  Textarea,
} from '@beacon/ui'
import type { ServerItem, ZoneTreeResponse } from '@beacon/devmock'

import AssignTargetTree from './assign-target-tree'

interface RezoneDialogProps {
  // 待改派的服务器；null 关闭
  server: ServerItem | null
  tree: ZoneTreeResponse | undefined
  pending: boolean
  errorText?: string | null
  // 确认：目标 id + 原因
  onConfirm: (targetId: number, reason: string) => void
  onOpenChange: (open: boolean) => void
}

export default function RezoneDialog({
  server,
  tree,
  pending,
  errorText,
  onConfirm,
  onOpenChange,
}: RezoneDialogProps) {
  const { t } = useTranslation()
  const [target, setTarget] = useState('')
  const [reason, setReason] = useState('')

  // 每次打开清空草稿
  useEffect(() => {
    if (server) {
      setTarget('')
      setReason('')
    }
  }, [server])

  const kind = server?.kind === 'proxy' ? 'proxy' : 'backend'
  const labelKey = kind === 'backend' ? 'cluster.zones.assign.targetZone' : 'cluster.zones.assign.targetCluster'
  // 目标不能与原归属相同（后端也会守卫，这里做前端提示性禁用）
  const currentId = kind === 'backend' ? server?.zoneId : server?.bcClusterId
  const sameAsCurrent = useMemo(
    () => target !== '' && currentId != null && Number.parseInt(target, 10) === currentId,
    [target, currentId],
  )
  const canConfirm = target !== '' && reason.trim() !== '' && !sameAsCurrent && !pending

  return (
    <Dialog open={server !== null} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('cluster.zones.drag.confirmRezoneTitle')}</DialogTitle>
        </DialogHeader>

        <div className="grid gap-3">
          {server && (
            <p className="font-mono text-sm text-ink-2">{server.serverId}</p>
          )}
          <div className="space-y-1.5">
            <Label>{t(labelKey)}</Label>
            <AssignTargetTree tree={tree} kind={kind} value={target} onChange={setTarget} />
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="rezone-picker-reason">{t('cluster.zones.drag.rezoneReasonLabel')}</Label>
            <Textarea
              id="rezone-picker-reason"
              value={reason}
              onChange={(e) => {
                setReason(e.target.value)
              }}
              placeholder={t('cluster.zones.drag.rezoneReasonPlaceholder')}
              rows={3}
            />
          </div>

          {errorText != null && errorText !== '' && <p className="text-sm text-crit">{errorText}</p>}
        </div>

        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => {
              onOpenChange(false)
            }}
          >
            {t('cluster.zones.drag.cancel')}
          </Button>
          <Button
            disabled={!canConfirm}
            onClick={() => {
              onConfirm(Number.parseInt(target, 10), reason.trim())
            }}
          >
            {t('cluster.zones.drag.confirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
