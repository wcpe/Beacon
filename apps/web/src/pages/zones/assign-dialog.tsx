// 批量首次分配弹窗：选目标（小区 / 集群）→ 影响预览 → 确认 → 逐台结果。
// 已分配 server 被 409 rezone_required 时提示走换区工单。

import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'

import {
  Button,
  Checkbox,
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Label,
  Textarea,
} from '@beacon/ui'
import type { ServerItem, ZoneTreeResponse } from '@beacon/contracts'

import type { ApprovalTicket } from '../../api/cluster'
import AssignTargetTree, { LOBBY_TARGET_PREFIX } from './assign-target-tree'

/** 从结构树按 id 找目标可读名（小区含集群 / 大区路径，集群直接用名）。 */
function targetLabelOf(
  tree: ZoneTreeResponse | undefined,
  kind: 'backend' | 'proxy',
  target: string,
  lobbyCluster: { id: number; name: string } | null,
  lobbyLabel: string,
): string {
  if (target === '') {
    return ''
  }
  // 大厅集群目标（FR-225 / A1）：值形如 lobby:<id>
  if (target.startsWith(LOBBY_TARGET_PREFIX)) {
    return lobbyCluster === null ? lobbyLabel : `${lobbyLabel} · ${lobbyCluster.name}`
  }
  if (!tree) {
    return ''
  }
  if (kind === 'proxy') {
    return tree.clusters.find((c) => String(c.id) === target)?.name ?? ''
  }
  for (const cluster of tree.clusters) {
    for (const region of cluster.regions) {
      for (const zone of region.zones) {
        if (String(zone.id) === target) {
          return `${cluster.name} / ${region.name} / ${zone.name}`
        }
      }
    }
  }
  return ''
}

interface AssignDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  // 待分配的 server（同 kind，由篮子保证）
  servers: ServerItem[]
  // 篮内 server 的 kind，决定目标是小区还是集群
  kind: 'backend' | 'proxy'
  // 结构树：目标选择器用可搜索树呈现
  tree: ZoneTreeResponse | undefined
  // 可选：当前 namespace 的大厅集群（backend 时可指派为大厅成员，FR-225 / A1）
  lobbyCluster?: { id: number; name: string } | null
  pending: boolean
  errorText?: string | null
  // 审批申请票据；创建后必须等待审批中心和 worker。
  approvalTicket: ApprovalTicket | null
  // targetId 为目标 id 字符串（小区 / 集群 id，或 lobby:<id>）
  onConfirm: (targetId: string, isDefaultEntry: boolean, reason: string) => void
}

export default function AssignDialog({
  open,
  onOpenChange,
  servers,
  kind,
  tree,
  lobbyCluster = null,
  pending,
  errorText,
  approvalTicket,
  onConfirm,
}: AssignDialogProps) {
  const { t } = useTranslation()
  const [target, setTarget] = useState('')
  const [isDefaultEntry, setIsDefaultEntry] = useState(false)
  const [reason, setReason] = useState('')

  // 每次打开清空草稿
  useEffect(() => {
    if (open) {
      setTarget('')
      setIsDefaultEntry(false)
      setReason('')
    }
  }, [open])

  const isLobbyTarget = target.startsWith('lobby:')
  const targetLabel = useMemo(
    () => targetLabelOf(tree, kind, target, lobbyCluster, t('cluster.zones.assign.targetLobby')),
    [tree, kind, target, lobbyCluster, t],
  )

  const targetLabelKey = kind === 'backend' ? 'cluster.zones.assign.targetZone' : 'cluster.zones.assign.targetCluster'

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('cluster.zones.assign.title')}</DialogTitle>
        </DialogHeader>

        <div className="grid gap-3">
          <div className="space-y-1.5">
            <Label>{t(targetLabelKey)}</Label>
            {/* 可搜索树选目标：不拍平成下拉，按 大厅 / 集群 → 大区 → 小区 / 代理 层级选择 */}
            <AssignTargetTree tree={tree} kind={kind} value={target} onChange={setTarget} lobbyCluster={lobbyCluster} />
          </div>

          {/* 大厅目标不适用「默认入口」（默认入口是小区级语义），仅小区目标可勾选 */}
          {kind === 'backend' && !isLobbyTarget && (
            <label className="flex items-center gap-2 text-sm">
              <Checkbox
                checked={isDefaultEntry}
                onCheckedChange={(value) => {
                  setIsDefaultEntry(value === true)
                }}
                aria-label={t('cluster.zones.assign.setDefaultEntry')}
              />
              {t('cluster.zones.assign.setDefaultEntry')}
            </label>
          )}

          {/* 影响预览 */}
          {target !== '' && (
            <div className="rounded-md border border-brand-100 bg-brand-50 px-3 py-2 text-sm text-brand-600">
              {t('cluster.zones.assign.previewLine', { count: servers.length, target: targetLabel })}
            </div>
          )}

          <div className="space-y-1.5">
            <Label htmlFor="assign-reason">申请原因</Label>
            <Textarea id="assign-reason" aria-label="申请原因" value={reason} onChange={(event) => { setReason(event.target.value) }} rows={2} />
          </div>

          {approvalTicket && (
            <div className="grid gap-1 rounded-md border border-brand-100 bg-brand-50 px-3 py-2 text-sm text-ink-2" role="status">
              <span>分配审批申请已创建，等待审批中心执行。</span>
              <Link className="w-fit text-brand hover:underline" to={`/approvals/${encodeURIComponent(approvalTicket.approvalRequestId)}`}>查看统一审批</Link>
            </div>
          )}

          {errorText && <p className="text-sm text-crit">{errorText}</p>}
        </div>

        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => {
              onOpenChange(false)
            }}
          >
            {t('cluster.zones.assign.cancel')}
          </Button>
          <Button
            disabled={target === '' || reason.trim() === '' || pending || approvalTicket !== null}
            onClick={() => {
              onConfirm(target, isDefaultEntry, reason.trim())
            }}
          >
            {t('cluster.zones.assign.confirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
