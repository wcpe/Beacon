import { useMemo, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'

import { Badge, Button, Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, Input, Label, Textarea } from '@beacon/ui'
import type { LifecycleAction, LifecycleStatus, NamespaceLifecycleImpact, ServerLifecycleImpact } from '@beacon/contracts'

import { createLifecycleApprovalRequest } from '../../api/approvals'
import { fetchServerLifecycleImpact } from '../../api/cluster'
import { fetchNamespaceLifecycleImpact, fetchNamespacePermanentDeletionImpact } from '../../api/system'
import { isDemoMode } from '../../demo-mode'

type LifecycleSubject = 'namespace' | 'server'
type Impact = NamespaceLifecycleImpact | ServerLifecycleImpact

interface LifecycleApprovalControlProps {
  subject: LifecycleSubject
  id: number
  stableID: string
  lifecycle?: LifecycleStatus
  onRequested?: () => void
}

export default function LifecycleApprovalControl({ subject, id, stableID, lifecycle = 'active', onRequested }: LifecycleApprovalControlProps) {
  const [open, setOpen] = useState(false)
  const [action, setAction] = useState<LifecycleAction>(lifecycle === 'archived' ? 'restore' : 'archive')
  const [reason, setReason] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [ticketID, setTicketID] = useState<string | null>(null)

  const impact = useQuery({
    queryKey: ['lifecycle-impact', subject, id, action],
    queryFn: () => loadImpact(subject, id, action),
    enabled: open && ticketID === null,
  })

  const submit = useMutation({
    mutationFn: () => createLifecycleApprovalRequest(requestBody(subject, id, action, reason, confirmation), lifecycleIdempotencyKey(subject, id, action)),
    onSuccess: (ticket) => {
      setTicketID(ticket.approvalRequestId)
      onRequested?.()
    },
  })

  if (isDemoMode()) {
    return null
  }
  if (lifecycle === 'tombstoned') {
    return <Badge variant="off">已墓碑</Badge>
  }

  const permanent = action === 'permanent-delete'
  const canSubmit = impact.data !== undefined && reason.trim() !== '' && (!permanent || confirmation === stableID) && !submit.isPending
  const availableActions = lifecycle === 'archived' ? ['restore', 'permanent-delete'] as LifecycleAction[] : ['archive'] as LifecycleAction[]

  return (
    <>
      <Button size="sm" variant="outline" onClick={() => { setOpen(true) }}>
        {lifecycle === 'archived' ? '生命周期操作' : '申请归档'}
      </Button>
      <Dialog open={open} onOpenChange={(next) => { closeDialog(next, setOpen, setReason, setConfirmation, setTicketID) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{subject === 'namespace' ? '命名空间生命周期审批' : '服务器生命周期审批'}</DialogTitle>
            <DialogDescription>先确认服务端影响预览，再创建统一审批申请；页面不会直接变更生命周期。</DialogDescription>
          </DialogHeader>
          {ticketID ? (
            <div className="grid gap-2 rounded-lg border border-brand-100 bg-brand-50 px-3 py-3 text-sm text-ink-2" role="status">
              <span>审批申请已创建，等待审批中心执行。</span>
              <Link className="w-fit text-brand underline-offset-4 hover:underline" to={`/approvals/${encodeURIComponent(ticketID)}`}>查看统一审批</Link>
            </div>
          ) : (
            <>
              <div className="flex flex-wrap gap-2">
                {availableActions.map((candidate) => (
                  <Button key={candidate} size="sm" variant={candidate === action ? 'secondary' : 'outline'} onClick={() => { setAction(candidate); setConfirmation('') }}>
                    {actionLabel(candidate)}
                  </Button>
                ))}
              </div>
              <ImpactPreview query={impact} />
              <div className="grid gap-1.5">
                <Label htmlFor={`lifecycle-reason-${subject}-${String(id)}`}>申请原因</Label>
                <Textarea id={`lifecycle-reason-${subject}-${String(id)}`} value={reason} onChange={(event) => { setReason(event.target.value) }} rows={2} />
              </div>
              {permanent && (
                <div className="grid gap-1.5">
                  <Label htmlFor={`lifecycle-confirm-${subject}-${String(id)}`}>请输入完整标识确认永久删除</Label>
                  <Input id={`lifecycle-confirm-${subject}-${String(id)}`} value={confirmation} onChange={(event) => { setConfirmation(event.target.value) }} placeholder={stableID} />
                  <p className="text-xs text-destructive">此操作不可恢复，历史仍保留，标识永不复用。</p>
                </div>
              )}
              {submit.error && <p className="text-sm text-destructive">{messageOf(submit.error)}</p>}
            </>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => { setOpen(false) }}>{ticketID ? '关闭' : '取消'}</Button>
            {!ticketID && <Button disabled={!canSubmit} onClick={() => { submit.mutate() }}>{submit.isPending ? '正在提交' : '创建审批申请'}</Button>}
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

function loadImpact(subject: LifecycleSubject, id: number, action: LifecycleAction): Promise<Impact> {
  if (subject === 'namespace') {
    return action === 'permanent-delete' ? fetchNamespacePermanentDeletionImpact(id) : fetchNamespaceLifecycleImpact(id, action)
  }
  return fetchServerLifecycleImpact(id, action)
}

function requestBody(subject: LifecycleSubject, id: number, action: LifecycleAction, reason: string, confirmation: string) {
  const suffix = action === 'permanent-delete' ? 'permanent_delete' : action
  if (subject === 'namespace') {
    return { operationKey: `namespace.${suffix}` as const, parameters: { namespaceId: id, confirmationCode: confirmation }, reason: reason.trim() }
  }
  return { operationKey: `server.${suffix}` as const, parameters: { serverRowId: id, confirmationServerId: confirmation }, reason: reason.trim() }
}

function lifecycleIdempotencyKey(subject: LifecycleSubject, id: number, action: LifecycleAction): string {
  return `${subject}-${String(id)}-${action}-${crypto.randomUUID()}`
}

function closeDialog(next: boolean, setOpen: (value: boolean) => void, setReason: (value: string) => void, setConfirmation: (value: string) => void, setTicketID: (value: string | null) => void) {
  setOpen(next)
  if (!next) {
    setReason('')
    setConfirmation('')
    setTicketID(null)
  }
}

function ImpactPreview({ query }: { query: ReturnType<typeof useQuery<Impact>> }) {
  const lines = useMemo(() => impactLines(query.data), [query.data])
  if (query.isLoading) {
    return <div className="grid gap-2 rounded-lg bg-surface-2 px-3 py-3" aria-label="正在加载影响预览"><div className="h-4 w-1/3 animate-pulse rounded bg-surface-3" /><div className="h-10 animate-pulse rounded bg-surface-3" /></div>
  }
  if (query.isError) {
    return <p className="rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive">{messageOf(query.error)}。请重新打开后重试。</p>
  }
  return <div className="grid gap-1.5 rounded-lg bg-surface-2 px-3 py-3 text-sm"><span className="font-medium text-ink-1">服务端影响预览</span>{lines.map((line) => <span key={line} className="text-ink-3">{line}</span>)}</div>
}

function impactLines(impact: Impact | undefined): string[] {
  if (impact === undefined) return []
  if ('summary' in impact) return impact.summary
  return [
    `身份绑定 ${String(impact.identityCount)}，活动身份 ${String(impact.activeIdentityCount)}`,
    `活动命令 ${String(impact.activeCommandCount)}，当前在线 ${impact.online ? '是' : '否'}`,
  ]
}

function actionLabel(action: LifecycleAction): string {
  if (action === 'archive') return '申请归档'
  if (action === 'restore') return '申请恢复'
  return '申请永久删除'
}

function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}
