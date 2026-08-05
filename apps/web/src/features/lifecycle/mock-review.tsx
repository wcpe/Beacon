import { useMemo, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'

import { Badge, Button, Input, Label, Textarea } from '@beacon/ui'

export type LifecycleReviewSubject = 'namespace' | 'server'

type ReviewState = 'normal' | 'empty' | 'loading' | 'error' | 'huge'
type LifecycleStatus = 'active' | 'archived' | 'tombstoned'
type LifecycleAction = 'archive' | 'restore' | 'permanent-delete'
type StatusFilter = LifecycleStatus | 'all'

interface LifecycleMockReviewProps {
  subject: LifecycleReviewSubject
}

interface LifecycleRecord {
  id: string
  status: LifecycleStatus
  detail: string
}

const reviewStates: ReviewState[] = ['normal', 'empty', 'loading', 'error', 'huge']

function recordsFor(subject: LifecycleReviewSubject): LifecycleRecord[] {
  if (subject === 'namespace') {
    return [
      { id: 'game-prod', status: 'active', detail: '生产命名空间，允许归档申请' },
      { id: 'game-legacy', status: 'archived', detail: '只读观察中，可申请恢复或永久删除' },
      { id: 'game-retired', status: 'tombstoned', detail: '墓碑保留，历史只读' },
    ]
  }
  return [
    { id: 'game-prod-01', status: 'active', detail: '生产服务器，允许归档申请' },
    { id: 'game-legacy-01', status: 'archived', detail: '只读观察中，可申请恢复或永久删除' },
    { id: 'game-retired-01', status: 'tombstoned', detail: '墓碑保留，历史只读' },
  ]
}

export default function LifecycleMockReview({ subject }: LifecycleMockReviewProps) {
  const { t } = useTranslation()
  const records = useMemo(() => recordsFor(subject), [subject])
  const [reviewState, setReviewState] = useState<ReviewState>('normal')
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all')
  const [selectedID, setSelectedID] = useState(records[0].id)
  const [action, setAction] = useState<LifecycleAction>('archive')
  const [reason, setReason] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [submitted, setSubmitted] = useState(false)
  const subjectLabel = t(`system.lifecycle.subject.${subject}`)
  const selected = records.find((record) => record.id === selectedID) ?? records[0]
  const visibleRecords = statusFilter === 'all' ? records : records.filter((record) => record.status === statusFilter)

  const selectRecord = (record: LifecycleRecord) => {
    setSelectedID(record.id)
    setAction(record.status === 'active' ? 'archive' : record.status === 'archived' ? 'restore' : 'archive')
    setReason('')
    setConfirmation('')
    setSubmitted(false)
  }

  const switchAction = (next: LifecycleAction) => {
    setAction(next)
    setReason('')
    setConfirmation('')
    setSubmitted(false)
  }

  if (reviewState === 'empty') {
    return <ReviewCard subjectLabel={subjectLabel} reviewState={reviewState} onStateChange={setReviewState}>
      <p className="rounded-lg bg-surface-2 px-3 py-4 text-sm text-ink-3">{t('system.lifecycle.empty', { subject: subjectLabel })}</p>
    </ReviewCard>
  }

  if (reviewState === 'loading') {
    return <ReviewCard subjectLabel={subjectLabel} reviewState={reviewState} onStateChange={setReviewState}>
      <div className="grid gap-2" aria-label={t('system.lifecycle.loadingImpact')}>
        <div className="h-5 w-2/5 animate-pulse rounded bg-surface-3" />
        <div className="h-16 animate-pulse rounded-lg bg-surface-2" />
        <div className="h-16 animate-pulse rounded-lg bg-surface-2" />
      </div>
      <Button disabled>{t('system.lifecycle.waitSnapshot')}</Button>
    </ReviewCard>
  }

  if (reviewState === 'error') {
    return <ReviewCard subjectLabel={subjectLabel} reviewState={reviewState} onStateChange={setReviewState}>
      <div className="grid gap-2 rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-3 text-sm text-destructive">
        <span className="font-medium">{t('system.lifecycle.driftTitle')}</span>
        <span>{t('system.lifecycle.driftDescription')}</span>
        <Button variant="outline" className="w-fit" onClick={() => { setReviewState('normal') }}>{t('system.lifecycle.previewAgain')}</Button>
      </div>
    </ReviewCard>
  }

  const permanent = action === 'permanent-delete'
  const canSubmit = reason.trim() !== '' && (!permanent || confirmation === selected.id)

  return (
    <ReviewCard subjectLabel={subjectLabel} reviewState={reviewState} onStateChange={setReviewState}>
      <LifecycleList
        records={visibleRecords}
        selectedID={selected.id}
        statusFilter={statusFilter}
        subjectLabel={subjectLabel}
        onFilterChange={setStatusFilter}
        onSelect={selectRecord}
      />

      {selected.status === 'tombstoned' ? (
        <TombstoneDetail subject={subject} subjectLabel={subjectLabel} />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2 rounded-lg bg-surface-2 px-3 py-2.5 text-sm">
            <Badge variant={selected.status === 'active' ? 'ok' : 'warn'}>{statusLabel(t, selected.status)}</Badge>
            <code className="text-xs text-ink-2">{selected.id}</code>
            <span className="text-ink-3">{selected.detail}</span>
          </div>
          {selected.status === 'archived' && (
            <div className="grid gap-2 rounded-lg border border-border bg-surface-1 px-3 py-3">
              <p className="text-sm text-ink-3">{t('system.lifecycle.archivedReadonly')}</p>
              <div className="flex flex-wrap gap-2">
                <Button size="sm" variant={action === 'restore' ? 'secondary' : 'outline'} onClick={() => { switchAction('restore') }}>
                  {t('system.lifecycle.restore')}
                </Button>
                <Button size="sm" variant={action === 'permanent-delete' ? 'secondary' : 'outline'} onClick={() => { switchAction('permanent-delete') }}>
                  {t('system.lifecycle.permanentDelete')}
                </Button>
              </div>
            </div>
          )}
          <ImpactPreview huge={reviewState === 'huge'} subject={subject} action={action} />
          <div className="grid gap-1.5">
            <Label htmlFor={`${subject}-lifecycle-reason`}>{t('system.lifecycle.reason')}</Label>
            <Textarea
              id={`${subject}-lifecycle-reason`}
              value={reason}
              onChange={(event) => { setReason(event.target.value) }}
              placeholder={t('system.lifecycle.reasonPlaceholder')}
              rows={2}
            />
          </div>
          {permanent && (
            <div className="grid gap-1.5">
              <Label htmlFor={`${subject}-lifecycle-confirmation`}>{t('system.lifecycle.confirmLabel', { subject: subjectLabel })}</Label>
              <Input
                id={`${subject}-lifecycle-confirmation`}
                value={confirmation}
                onChange={(event) => { setConfirmation(event.target.value) }}
                placeholder={selected.id}
              />
              <p className="text-xs text-destructive">{permanentWarning(t, subject)}</p>
            </div>
          )}
          <div className="flex flex-wrap items-center gap-2">
            <Button disabled={!canSubmit} onClick={() => { setSubmitted(true) }}>{actionLabel(t, action)}</Button>
            {selected.status === 'active' && <span className="text-xs text-ink-4">{t('system.lifecycle.archiveHint')}</span>}
          </div>
          {submitted && (
            <div className="rounded-lg border border-brand-100 bg-brand-50 px-3 py-2.5 text-sm text-ink-2" role="status">
              {t('system.lifecycle.submitted', { action: actionLabel(t, action) })}{' '}
              <Link className="text-brand underline-offset-4 hover:underline" to="/approvals">{t('system.lifecycle.viewApprovals')}</Link>
            </div>
          )}
        </>
      )}
    </ReviewCard>
  )
}

function LifecycleList({
  records,
  selectedID,
  statusFilter,
  subjectLabel,
  onFilterChange,
  onSelect,
}: {
  records: LifecycleRecord[]
  selectedID: string
  statusFilter: StatusFilter
  subjectLabel: string
  onFilterChange: (status: StatusFilter) => void
  onSelect: (record: LifecycleRecord) => void
}) {
  const { t } = useTranslation()
  return (
    <div className="grid gap-2 rounded-lg border border-border bg-surface-1 p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-sm font-medium text-ink-1">{t('system.lifecycle.listTitle', { subject: subjectLabel })}</span>
        <label className="flex items-center gap-2 text-xs text-ink-3">
          {t('system.lifecycle.filter')}
          <select
            aria-label={t('system.lifecycle.filter')}
            className="rounded border border-border bg-surface-1 px-2 py-1 text-ink-2"
            value={statusFilter}
            onChange={(event) => { onFilterChange(event.target.value as StatusFilter) }}
          >
            <option value="all">{t('system.lifecycle.filterAll')}</option>
            <option value="active">{statusLabel(t, 'active')}</option>
            <option value="archived">{statusLabel(t, 'archived')}</option>
            <option value="tombstoned">{statusLabel(t, 'tombstoned')}</option>
          </select>
        </label>
      </div>
      {records.length === 0 ? (
        <p className="text-sm text-ink-3">{t('system.lifecycle.filteredEmpty')}</p>
      ) : records.map((record) => (
        <button
          key={record.id}
          type="button"
          className={`flex w-full items-center gap-2 rounded-md px-2.5 py-2 text-left text-sm ${record.id === selectedID ? 'bg-brand-50' : 'bg-surface-2 hover:bg-surface-3'}`}
          onClick={() => { onSelect(record) }}
        >
          <code className="text-xs text-ink-2">{record.id}</code>
          <Badge variant={record.status === 'active' ? 'ok' : record.status === 'archived' ? 'warn' : 'off'}>{statusLabel(t, record.status)}</Badge>
          <span className="ml-auto text-xs text-ink-4">{record.detail}</span>
        </button>
      ))}
    </div>
  )
}

function TombstoneDetail({ subject, subjectLabel }: { subject: LifecycleReviewSubject; subjectLabel: string }) {
  const { t } = useTranslation()
  return (
    <div className="grid gap-2 rounded-lg border border-border bg-surface-2 px-3 py-3 text-sm">
      <span className="font-medium text-ink-1">{t('system.lifecycle.tombstoneTitle')}</span>
      <span className="text-ink-3">{subject === 'namespace' ? t('system.lifecycle.namespaceTombstone') : t('system.lifecycle.serverTombstone')}</span>
      <span className="text-xs text-ink-4">{t('system.lifecycle.noMoreActions', { subject: subjectLabel })}</span>
      <Link className="w-fit text-sm text-brand underline-offset-4 hover:underline" to="/approvals">{t('system.lifecycle.viewApprovals')}</Link>
    </div>
  )
}

function ReviewCard({ subjectLabel, reviewState, onStateChange, children }: {
  subjectLabel: string
  reviewState: ReviewState
  onStateChange: (state: ReviewState) => void
  children: ReactNode
}) {
  const { t } = useTranslation()
  return (
    <section className="grid gap-3 rounded-xl border border-dashed border-brand-200 bg-brand-50/30 p-4" aria-label={t('system.lifecycle.mockLabel', { subject: subjectLabel })}>
      <div className="flex flex-wrap items-center gap-2">
        <div>
          <h2 className="text-sm font-semibold text-ink-1">{t('system.lifecycle.title', { subject: subjectLabel })}</h2>
          <p className="mt-0.5 text-xs text-ink-3">{t('system.lifecycle.demoOnly')}</p>
        </div>
        <div className="ml-auto flex flex-wrap gap-1" aria-label={t('system.lifecycle.scenarios')}>
          {reviewStates.map((state) => (
            <Button key={state} size="sm" variant={state === reviewState ? 'secondary' : 'ghost'} onClick={() => { onStateChange(state) }}>
              {t(`system.lifecycle.states.${state}`)}
            </Button>
          ))}
        </div>
      </div>
      {children}
    </section>
  )
}

function ImpactPreview({ huge, subject, action }: { huge: boolean; subject: LifecycleReviewSubject; action: LifecycleAction }) {
  const { t } = useTranslation()
  const impactNames = subject === 'namespace'
    ? ['servers', 'topology', 'identities', 'environmentTrust', 'tasks']
    : ['identityBinding', 'topology', 'tasks']
  const counts = subject === 'namespace' ? [12, 8, 14, 5, 3] : [1, 1, 3]
  return (
    <div className="grid gap-2 rounded-lg border border-border bg-surface-1 p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-sm font-medium text-ink-1">{t('system.lifecycle.impactTitle')}</span>
        <span className="text-xs text-ink-4">{t('system.lifecycle.snapshotFrozen')}</span>
      </div>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
        {impactNames.map((name, index) => (
          <div key={name} className="rounded-md bg-surface-2 px-2.5 py-2">
            <span className="block text-[11px] text-ink-4">{t(`system.lifecycle.impacts.${name}`)}</span>
            <span className="text-sm font-semibold text-ink-1">{huge ? `${(counts[index] * 10_000).toLocaleString()}+` : counts[index]}</span>
          </div>
        ))}
      </div>
      <p className="text-xs text-ink-3">{impactDescription(t, subject, action, huge)}</p>
    </div>
  )
}

function statusLabel(t: (key: string) => string, status: LifecycleStatus) {
  return t(`system.lifecycle.status.${status}`)
}

function actionLabel(t: (key: string) => string, action: LifecycleAction) {
  return t(`system.lifecycle.actions.${action}`)
}

function impactDescription(t: (key: string) => string, subject: LifecycleReviewSubject, action: LifecycleAction, huge: boolean) {
  if (huge) return t('system.lifecycle.hugeImpact')
  if (action === 'permanent-delete') return t(subject === 'namespace' ? 'system.lifecycle.namespacePermanentImpact' : 'system.lifecycle.serverPermanentImpact')
  if (action === 'restore') return t('system.lifecycle.restoreImpact')
  return t('system.lifecycle.archiveImpact')
}

function permanentWarning(t: (key: string) => string, subject: LifecycleReviewSubject) {
  return t(subject === 'namespace' ? 'system.lifecycle.namespacePermanentWarning' : 'system.lifecycle.serverPermanentWarning')
}
