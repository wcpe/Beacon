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
import type { AssignmentResult, ServerItem, ZoneTreeResponse } from '@beacon/devmock'

// 目标选项：value 为目标 id（字符串），label 为可读名称。
export interface TargetOption {
  value: string
  label: string
}

/** 从结构树派生目标选项：backend 落小区（zone id），proxy 落集群（cluster id）。 */
export function targetOptionsOf(tree: ZoneTreeResponse | undefined, kind: 'backend' | 'proxy'): TargetOption[] {
  if (!tree) {
    return []
  }
  if (kind === 'proxy') {
    return tree.clusters.map((cluster) => ({ value: String(cluster.id), label: cluster.name }))
  }
  const options: TargetOption[] = []
  for (const cluster of tree.clusters) {
    for (const region of cluster.regions) {
      for (const zone of region.zones) {
        options.push({ value: String(zone.id), label: `${cluster.name} / ${region.name} / ${zone.name}` })
      }
    }
  }
  return options
}

interface AssignDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  // 待分配的 server（同 kind，由篮子保证）
  servers: ServerItem[]
  // 篮内 server 的 kind，决定目标是小区还是集群
  kind: 'backend' | 'proxy'
  options: TargetOption[]
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
  options,
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

  const targetLabel = useMemo(
    () => options.find((o) => o.value === target)?.label ?? '',
    [options, target],
  )

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
            <Label htmlFor="assign-target">{t(targetLabelKey)}</Label>
            {/* 用原生 select：候选来自结构树，严格选已存在项 */}
            <select
              id="assign-target"
              aria-label={t(targetLabelKey)}
              value={target}
              onChange={(e) => {
                setTarget(e.target.value)
              }}
              className="h-9 w-full rounded-md border border-border bg-card px-2 text-sm text-ink-1"
            >
              <option value="">—</option>
              {options.map((opt) => (
                <option key={opt.value} value={opt.value}>
                  {opt.label}
                </option>
              ))}
            </select>
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
