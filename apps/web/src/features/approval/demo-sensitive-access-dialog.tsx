import { useRef, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'

import { Button, Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, Label, Textarea } from '@beacon/ui'
import type { ApprovalRequest } from '@beacon/contracts'

import { fetchApprovalDetail } from '../../api/approvals'
import { ApiClientError } from '../../api/http'

interface SensitiveAccessApprovalResponse {
  requestId: string
  status: string
}

interface DemoSensitiveAccessDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  mode: 'demo' | 'real'
  targetRef: string
  createApproval: (reason: string, idempotencyKey: string) => Promise<SensitiveAccessApprovalResponse>
  consumeApproval: (approval: ApprovalRequest) => Promise<void>
}

function errorMessage(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

export default function DemoSensitiveAccessDialog({ open, onOpenChange, mode, targetRef, createApproval, consumeApproval }: DemoSensitiveAccessDialogProps) {
  const { t } = useTranslation()
  const [reason, setReason] = useState('')
  const [requestId, setRequestId] = useState<string | null>(null)
  const idempotencyKey = useRef<string | null>(null)
  const key = mode === 'demo' ? 'approvals.demo' : 'approvals.access'
  const createMutation = useMutation({
    mutationFn: () => {
      idempotencyKey.current ??= crypto.randomUUID()
      return createApproval(reason.trim(), idempotencyKey.current)
    },
    onSuccess: (approval) => {
      setRequestId(approval.requestId)
    },
  })
  const approvalQuery = useQuery({
    queryKey: ['demo-sensitive-access', requestId],
    queryFn: () => {
      if (requestId === null) return Promise.reject(new Error('演示审批申请尚未创建'))
      return fetchApprovalDetail(requestId)
    },
    enabled: requestId !== null,
    refetchInterval: (query) => (query.state.data?.status === 'executing' ? 100 : false),
  })
  const consumeMutation = useMutation({
    mutationFn: () => {
      if (requestId === null || approval === undefined) return Promise.reject(new Error('审批申请尚未就绪'))
      return consumeApproval(approval)
    },
  })
  const approval = approvalQuery.data
  const status = approval?.status
  const canSubmit = reason.trim() !== '' && !createMutation.isPending
  const error = createMutation.error ?? approvalQuery.error ?? consumeMutation.error

  const close = (): void => {
    setReason('')
    setRequestId(null)
    idempotencyKey.current = null
    createMutation.reset()
    consumeMutation.reset()
    onOpenChange(false)
  }

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) close() }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t(`${key}.title`)}</DialogTitle>
          <DialogDescription>{t(`${key}.desc`)}</DialogDescription>
        </DialogHeader>
        <p className="break-all rounded-md bg-surface-2 px-3 py-2 font-mono text-xs text-ink-3">
          {t(`${key}.target`)}：{targetRef}
        </p>
        {requestId === null ? (
          <div className="grid gap-1.5">
            <Label htmlFor="demo-sensitive-access-reason">{t(`${key}.reasonLabel`)}</Label>
            <Textarea
              id="demo-sensitive-access-reason"
              value={reason}
              onChange={(event) => { setReason(event.target.value) }}
              placeholder={t(`${key}.reasonPlaceholder`)}
              rows={3}
            />
          </div>
        ) : (
          <div className="grid gap-2 rounded-lg border border-border bg-surface-1 p-3 text-sm" role="status">
            {status === 'pending' && <p>{t(`${key}.pending`)}</p>}
            {status === 'executing' && <p>{t(`${key}.executing`)}</p>}
            {status === 'succeeded' && <p>{t(`${key}.succeeded`)}</p>}
            {status === 'failed' && <p className="text-destructive">{t(`${key}.failed`)}</p>}
            {approvalQuery.isLoading && <div className="h-5 w-3/5 animate-pulse rounded bg-surface-3" />}
            <Link className="w-fit text-brand underline-offset-4 hover:underline" to={`/approvals/${encodeURIComponent(requestId)}`}>
              {t(`${key}.open`)}
            </Link>
          </div>
        )}
        {consumeMutation.isSuccess && (
          <p className="rounded-lg border border-ok-bd bg-ok-bg px-3 py-2 text-sm text-ok">{t(`${key}.consumed`)}</p>
        )}
        {error && <p className="text-sm text-destructive">{errorMessage(error)}</p>}
        <DialogFooter>
          {requestId === null ? (
            <Button disabled={!canSubmit} onClick={() => { createMutation.mutate() }}>
              {createMutation.isPending ? t(`${key}.submitting`) : t(`${key}.submit`)}
            </Button>
          ) : (
            <>
              {status !== 'succeeded' && (
                <Button variant="outline" disabled={approvalQuery.isFetching} onClick={() => { void approvalQuery.refetch() }}>
                  {t(`${key}.refresh`)}
                </Button>
              )}
              {status === 'succeeded' && !consumeMutation.isSuccess && (
                <Button disabled={consumeMutation.isPending} onClick={() => { consumeMutation.mutate() }}>
                  {consumeMutation.isPending ? t(`${key}.consuming`) : t(`${key}.consume`)}
                </Button>
              )}
            </>
          )}
          <Button variant="outline" onClick={close}>{t(`${key}.close`)}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
