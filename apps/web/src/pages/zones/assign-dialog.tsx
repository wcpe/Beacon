// 批量首次分配弹窗：选目标（小区 / 集群）→ 影响预览 → 确认 → 逐台结果。
// 已分配 server 被 409 rezone_required 时提示走换区工单。

import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Button,
  Checkbox,
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Label,
} from '@beacon/ui'
import type { AssignmentResult, ServerItem, ZoneTreeResponse } from '@beacon/contracts'

import AssignTargetTree from './assign-target-tree'

/** 从结构树按 id 找目标可读名（小区含集群 / 大区路径，集群直接用名）。 */
function targetLabelOf(tree: ZoneTreeResponse | undefined, kind: 'backend' | 'proxy', target: string): string {
  if (!tree || target === '') {
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
  pending: boolean
  errorText?: string | null
  // 逐台结果（成功后展示）；null 表示尚未提交
  results: AssignmentResult[] | null
  // targetId 为目标 id 字符串
  onConfirm: (targetId: string, isDefaultEntry: boolean) => void
}

export default function AssignDialog({
  open,
  onOpenChange,
  servers,
  kind,
  tree,
  pending,
  errorText,
  results,
  onConfirm,
}: AssignDialogProps) {
  const { t } = useTranslation()
  const [target, setTarget] = useState('')
  const [isDefaultEntry, setIsDefaultEntry] = useState(false)

  // 每次打开清空草稿
  useEffect(() => {
    if (open) {
      setTarget('')
      setIsDefaultEntry(false)
    }
  }, [open])

  const targetLabel = useMemo(() => targetLabelOf(tree, kind, target), [tree, kind, target])

  const failed = results?.filter((r) => !r.ok) ?? []
  const succeeded = results?.filter((r) => r.ok) ?? []
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
            {/* 可搜索树选目标：不拍平成下拉，按 集群 → 大区 → 小区 / 代理 层级选择 */}
            <AssignTargetTree tree={tree} kind={kind} value={target} onChange={setTarget} />
          </div>

          {kind === 'backend' && (
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

          {/* 逐台结果 */}
          {results && (
            <div className="grid gap-1 rounded-md border border-border px-3 py-2 text-sm">
              <p className="font-semibold text-ink-1">{t('cluster.zones.assign.resultTitle')}</p>
              {succeeded.length > 0 && (
                <p className="text-ok">{t('cluster.zones.assign.resultOk', { count: succeeded.length })}</p>
              )}
              {failed.length > 0 && (
                <>
                  <p className="text-crit">{t('cluster.zones.assign.resultFail', { count: failed.length })}</p>
                  <ul className="list-disc pl-5 text-ink-3">
                    {failed.map((r) => (
                      <li key={r.id}>
                        {r.serverId} ·{' '}
                        {r.code === 'rezone_required'
                          ? t('cluster.zones.assign.rezoneRequired')
                          : t(`cluster.zones.rezoneCode.${r.code ?? ''}`, r.code ?? '')}
                      </li>
                    ))}
                  </ul>
                </>
              )}
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
            disabled={target === '' || pending}
            onClick={() => {
              onConfirm(target, isDefaultEntry)
            }}
          >
            {t('cluster.zones.assign.confirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
