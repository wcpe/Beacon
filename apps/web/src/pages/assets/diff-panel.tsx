// 两侧差异面板：左右文件分别提审；双侧成功后只消费并展示服务端生成的元数据差异摘要。
import { useRef, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { SplitSquareHorizontal } from 'lucide-react'

import { Badge, Button, Input, Label, SectionHeader, Textarea } from '@beacon/ui'
import type { ApprovalRequest } from '@beacon/contracts'

import { fetchApprovalDetail } from '../../api/approvals'
import {
  assetReadCommandId,
  consumeAssetPairReadGrant,
  requestAssetPairReadApprovals,
  type AssetPairDiffSummary,
  type AssetPairReadApprovalResponse,
} from '../../api/delivery-assets'
import { ApiClientError } from '../../api/http'
import { formatBytes, shortHash } from './format'

interface ApprovalPair {
  leftRequestId: string
  rightRequestId: string
}

function approvalState(approval: ApprovalRequest | undefined): 'pending' | 'ready' | 'unavailable' {
  if (approval === undefined || approval.status === 'pending' || approval.status === 'executing') return 'pending'
  return approval.status === 'succeeded' && approval.sensitiveAccessGrant !== undefined ? 'ready' : 'unavailable'
}

function errorMessage(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

export default function DiffPanel() {
  const { t } = useTranslation()
  const [leftServer, setLeftServer] = useState('')
  const [rightServer, setRightServer] = useState('')
  const [path, setPath] = useState('')
  const [reason, setReason] = useState('')
  const [approvalPair, setApprovalPair] = useState<ApprovalPair | null>(null)
  const [summary, setSummary] = useState<AssetPairDiffSummary | null>(null)
  const idempotencyKey = useRef<string | null>(null)
  const canRequest = leftServer.trim() !== '' && rightServer.trim() !== '' && path.trim() !== '' && reason.trim() !== ''

  const createMutation = useMutation({
    mutationFn: async (): Promise<AssetPairReadApprovalResponse> => {
      idempotencyKey.current ??= crypto.randomUUID()
      return requestAssetPairReadApprovals(
        { serverId: leftServer.trim(), path: path.trim() },
        { serverId: rightServer.trim(), path: path.trim() },
        reason.trim(),
        idempotencyKey.current,
      )
    },
    onSuccess: (created) => {
      setApprovalPair({ leftRequestId: created.leftRequestId, rightRequestId: created.rightRequestId })
      setSummary(null)
    },
  })
  const leftApprovalQuery = useQuery({
    queryKey: ['asset-pair-read-approval', approvalPair?.leftRequestId],
    queryFn: () => fetchApprovalDetail(approvalPair?.leftRequestId ?? ''),
    enabled: approvalPair !== null,
    refetchInterval: (query) => (query.state.data?.status === 'executing' ? 100 : false),
  })
  const rightApprovalQuery = useQuery({
    queryKey: ['asset-pair-read-approval', approvalPair?.rightRequestId],
    queryFn: () => fetchApprovalDetail(approvalPair?.rightRequestId ?? ''),
    enabled: approvalPair !== null,
    refetchInterval: (query) => (query.state.data?.status === 'executing' ? 100 : false),
  })
  const leftState = approvalState(leftApprovalQuery.data)
  const rightState = approvalState(rightApprovalQuery.data)
  const consumeMutation = useMutation({
    mutationFn: () => {
      const left = leftApprovalQuery.data
      const right = rightApprovalQuery.data
      const grantId = left?.sensitiveAccessGrant?.grantId
      const commandId = assetReadCommandId(left?.resultRef)
      if (left === undefined || right === undefined || leftState !== 'ready' || rightState !== 'ready' || !grantId || commandId === null) {
        return Promise.reject(new Error('两侧审批与授权尚未就绪'))
      }
      return consumeAssetPairReadGrant(grantId, commandId)
    },
    onSuccess: setSummary,
  })
  const error = createMutation.error ?? leftApprovalQuery.error ?? rightApprovalQuery.error ?? consumeMutation.error

  const requestApprovals = (): void => {
    setApprovalPair(null)
    setSummary(null)
    idempotencyKey.current = null
    createMutation.reset()
    consumeMutation.reset()
    createMutation.mutate()
  }

  return (
    <section className="grid gap-3" aria-label={t('delivery.assets.diff.title')}>
      <SectionHeader
        icon={<SplitSquareHorizontal className="size-4" aria-hidden />}
        title={t('delivery.assets.diff.title')}
      />
      <p className="text-sm text-ink-3">{t('delivery.assets.diff.hint')}</p>

      <div className="flex flex-wrap items-end gap-2">
        <div className="grid gap-1.5">
          <Label htmlFor="diff-left">{t('delivery.assets.diff.leftServer')}</Label>
          <Input id="diff-left" aria-label={t('delivery.assets.diff.leftServer')} value={leftServer} onChange={(e) => { setLeftServer(e.target.value) }} className="w-40 font-mono" />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="diff-right">{t('delivery.assets.diff.rightServer')}</Label>
          <Input id="diff-right" aria-label={t('delivery.assets.diff.rightServer')} value={rightServer} onChange={(e) => { setRightServer(e.target.value) }} className="w-40 font-mono" />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="diff-path">{t('delivery.assets.diff.pathLabel')}</Label>
          <Input id="diff-path" aria-label={t('delivery.assets.diff.pathLabel')} value={path} onChange={(e) => { setPath(e.target.value) }} className="w-80 font-mono" />
        </div>
      </div>
      <div className="grid max-w-xl gap-1.5">
        <Label htmlFor="diff-reason">{t('delivery.assets.diff.reasonLabel')}</Label>
        <Textarea id="diff-reason" aria-label={t('delivery.assets.diff.reasonLabel')} value={reason} onChange={(e) => { setReason(e.target.value) }} placeholder={t('delivery.assets.diff.reasonPlaceholder')} rows={2} />
      </div>
      <div className="flex flex-wrap gap-2">
        <Button disabled={!canRequest || createMutation.isPending} onClick={requestApprovals}>
          {createMutation.isPending ? t('delivery.assets.diff.requesting') : t('delivery.assets.diff.request')}
        </Button>
        {approvalPair !== null && (
          <Button variant="outline" disabled={leftApprovalQuery.isFetching || rightApprovalQuery.isFetching} onClick={() => { void Promise.all([leftApprovalQuery.refetch(), rightApprovalQuery.refetch()]) }}>
            {t('delivery.assets.diff.refresh')}
          </Button>
        )}
      </div>

      <p className="text-sm text-ink-3">{t('delivery.assets.diff.approvalHint')}</p>
      {error && <p className="text-sm text-destructive">{errorMessage(error)}</p>}
      {approvalPair !== null && (
        <div className="grid gap-2 rounded-lg border border-border bg-surface-1 p-3" aria-label={t('delivery.assets.diff.approvalStatus')}>
          <ApprovalSide label={t('delivery.assets.diff.leftHeading')} requestId={approvalPair.leftRequestId} state={leftState} />
          <ApprovalSide label={t('delivery.assets.diff.rightHeading')} requestId={approvalPair.rightRequestId} state={rightState} />
          {leftState === 'ready' && rightState === 'ready' && summary === null && (
            <Button className="w-fit" disabled={consumeMutation.isPending} onClick={() => { consumeMutation.mutate() }}>
              {consumeMutation.isPending ? t('delivery.assets.diff.consuming') : t('delivery.assets.diff.consume')}
            </Button>
          )}
        </div>
      )}
      {summary !== null && <DiffSummary summary={summary} />}
    </section>
  )
}

function ApprovalSide({ label, requestId, state }: { label: string; requestId: string; state: 'pending' | 'ready' | 'unavailable' }) {
  const { t } = useTranslation()
  const variant = state === 'ready' ? 'ok' : state === 'unavailable' ? 'crit' : 'off'
  return (
    <div className="flex flex-wrap items-center gap-2 text-sm">
      <span className="font-medium">{label}</span>
      <Badge variant={variant}>{t(`delivery.assets.diff.approvalState.${state}`)}</Badge>
      <Link className="text-brand underline-offset-4 hover:underline" to={`/approvals/${encodeURIComponent(requestId)}`}>
        {t('delivery.assets.diff.openApproval')}
      </Link>
    </div>
  )
}

function DiffSummary({ summary }: { summary: AssetPairDiffSummary }) {
  const { t } = useTranslation()
  return (
    <div className="grid gap-2 rounded-lg border border-border bg-card p-3" aria-label={t('delivery.assets.diff.summaryTitle')}>
      <Badge className="w-fit" variant={summary.identical ? 'ok' : 'warn'}>
        {summary.unsupported ? t('delivery.assets.diff.unsupported') : summary.identical ? t('delivery.assets.diff.identical') : t('delivery.assets.diff.changed')}
      </Badge>
      <p className="text-sm text-ink-3">{t('delivery.assets.diff.summaryHint')}</p>
      {[summary.left, summary.right].map((side) => (
        <div key={side.serverId} className="grid gap-1 text-xs text-ink-3">
          <span className="font-mono text-ink-2">{side.serverId}:{side.path}</span>
          <span>{t('delivery.assets.diff.summaryMeta', { sha256: shortHash(side.sha256), size: formatBytes(side.size) })}</span>
        </div>
      ))}
    </div>
  )
}
