import {
  useEffect,
  useDeferredValue,
  useMemo,
  useState,
  type Dispatch,
  type ReactNode,
  type SetStateAction,
} from 'react'
import type { TFunction } from 'i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, ListChecks, Play, Square, Pause, RotateCcw } from 'lucide-react'
import {
  createFileSyncTask,
  listInstances,
  pauseFileSyncTask,
  planFileSyncTask,
  resumeFileSyncTask,
  startFileSyncTask,
  streamFileSyncTaskEvents,
  terminateFileSyncTask,
} from '@/api/client'
import type {
  FileSyncEvent,
  FileSyncTargetStatus,
  FileSyncTaskView,
  FileSyncTargetView,
  InstanceView,
} from '@/api/types'
import { formatBytes, formatTime } from '@/api/format'
import { usePageHeader } from '@/components/PageHeader'
import { useEnvironment } from '@/state/environment'
import { useMessage } from '@/components/useMessage'
import { AsyncSection } from '@beacon/ui'
import { SummaryStrip, type SummaryItem } from '@beacon/ui'
import { TableSkeleton } from '@beacon/ui'
import { Button } from '@beacon/ui'
import { Card, CardContent, CardHeader, CardTitle } from '@beacon/ui'
import { Input } from '@beacon/ui'
import { Label } from '@beacon/ui'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@beacon/ui'
import { cn } from '@/lib/utils'
import { Combobox } from '@beacon/ui'
import {
  filterFileSyncTargets,
  filterInstancesByKeyword,
  mergeSelectedIds,
  removeSelectedIds,
} from '@/lib/instanceFiltering'

const DEFAULT_DIRECTORY = 'plugins/AllinCore'
const DEFAULT_BATCH_SIZE = 20
const DEFAULT_INTERVAL_SEC = 30
const DEFAULT_FAILURE_THRESHOLD = 20
const TARGET_TABLE_PAGE_SIZE = 25
const SELECTED_SUMMARY_LIMIT = 6
const FILE_SYNC_TABLE_PAGE_SIZE = 25
const ALL_TARGET_STATUSES: FileSyncTargetStatus[] = [
  'pending',
  'manifesting',
  'backing-up',
  'transferring',
  'applying',
  'succeeded',
  'failed',
  'skipped',
]

type TaskAction = 'start' | 'pause' | 'resume' | 'terminate'

export default function ConfigSyncCenterPage() {
  const { t } = useTranslation()
  const namespace = useEnvironment()
  const msg = useMessage()
  const qc = useQueryClient()
  const [sourceServerId, setSourceServerId] = useState('')
  const [directory, setDirectory] = useState(DEFAULT_DIRECTORY)
  const [batchSize, setBatchSize] = useState(DEFAULT_BATCH_SIZE)
  const [intervalSec, setIntervalSec] = useState(DEFAULT_INTERVAL_SEC)
  const [failureThreshold, setFailureThreshold] = useState(DEFAULT_FAILURE_THRESHOLD)
  const [selectedTargets, setSelectedTargets] = useState<string[]>([])
  const [activeTask, setActiveTask] = useState<FileSyncTaskView | null>(null)
  const [eventAfterLogId, setEventAfterLogId] = useState(0)

  usePageHeader({
    title: t('fileSync.title'),
    subtitle: t('fileSync.subtitle'),
    envScoped: true,
  })

  const instancesQuery = useQuery({
    queryKey: ['file-sync-instances', namespace],
    queryFn: () => listInstances({ namespace: namespace || undefined }),
  })

  const onlineBukkit = useMemo(
    () => (instancesQuery.data ?? []).filter((i) => i.role === 'bukkit' && i.status === 'online'),
    [instancesQuery.data],
  )
  const sourceOptions = onlineBukkit
  const targetOptions = useMemo(
    () => onlineBukkit.filter((i) => i.serverId !== sourceServerId),
    [onlineBukkit, sourceServerId],
  )
  const detailPreviewTargets = useMemo(() => {
    if (selectedTargets.length === 0) return targetOptions
    const selected = new Set(selectedTargets)
    return targetOptions.filter((target) => selected.has(target.serverId))
  }, [selectedTargets, targetOptions])

  useEffect(() => {
    if (!sourceServerId && sourceOptions.length > 0) setSourceServerId(sourceOptions[0].serverId)
  }, [sourceOptions, sourceServerId])

  useEffect(() => {
    const available = new Set(targetOptions.map((i) => i.serverId))
    setSelectedTargets((prev) => {
      const next = prev.filter((id) => available.has(id))
      return next.length === prev.length ? prev : next
    })
  }, [targetOptions])

  useEffect(() => {
    if (!activeTask?.id) return
    const controller = new AbortController()
    streamFileSyncTaskEvents(activeTask.id, (event) => applyEvent(setActiveTask, event), {
      signal: controller.signal,
      afterLogId: eventAfterLogId,
    }).catch((e: Error) => {
      if (!controller.signal.aborted) msg.showError(e.message)
    })
    return () => controller.abort()
  }, [activeTask?.id, eventAfterLogId, msg])

  const planMut = useMutation({
    mutationFn: async () => {
      const created = await createFileSyncTask({
        namespace: namespace || 'prod',
        sourceServerId,
        directory: directory.trim(),
        batchSize,
        intervalSec,
        failureThresholdPercent: failureThreshold,
      })
      return planFileSyncTask(created.id, { targetServerIds: selectedTargets })
    },
    onSuccess: (task) => {
      setEventAfterLogId(lastFileSyncLogId(task))
      setActiveTask(task)
      qc.invalidateQueries({ queryKey: ['file-sync-tasks'] })
      msg.showSuccess(t('fileSync.targetPlanned', { count: task.plannedTargets }))
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  const actionMut = useMutation({
    mutationFn: ({ id, action }: { id: string; action: TaskAction }) => runTaskAction(id, action),
    onSuccess: (task) => {
      setActiveTask(task)
      msg.showSuccess(t('fileSync.actionDone'))
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onPlan() {
    if (!sourceServerId) return msg.showError(t('fileSync.needSource'))
    if (!directory.trim()) return msg.showError(t('fileSync.needDirectory'))
    if (selectedTargets.length === 0) return msg.showError(t('fileSync.needTarget'))
    planMut.mutate()
  }

  const summary = buildSummary(t, activeTask, detailPreviewTargets.length, batchSize)
  const busy = planMut.isPending || actionMut.isPending

  return (
    <div className="grid min-h-full grid-rows-[auto_auto_minmax(28rem,auto)_minmax(24rem,auto)] gap-3 pb-4">
      <SyncToolbar
        sourceOptions={sourceOptions}
        sourceServerId={sourceServerId}
        directory={directory}
        batchSize={batchSize}
        intervalSec={intervalSec}
        failureThreshold={failureThreshold}
        busy={busy}
        hasTargets={targetOptions.length > 0}
        task={activeTask}
        onSourceChange={setSourceServerId}
        onDirectoryChange={setDirectory}
        onBatchSizeChange={setBatchSize}
        onIntervalChange={setIntervalSec}
        onFailureThresholdChange={setFailureThreshold}
        onPlan={onPlan}
        onTaskAction={(action) => activeTask && actionMut.mutate({ id: activeTask.id, action })}
      />

      <div className="shrink-0 overflow-hidden [&>div]:flex-nowrap">
        <SummaryStrip items={summary} />
        {activeTask?.lastError && <ErrorBanner message={activeTask.lastError} />}
      </div>

      <div className="grid min-h-[28rem] grid-cols-1 gap-3 xl:grid-cols-[22rem_minmax(0,1fr)_22rem]">
        <TargetPanel
          targets={targetOptions}
          selected={selectedTargets}
          onSelectedChange={setSelectedTargets}
          isLoading={instancesQuery.isLoading}
          isError={instancesQuery.isError}
          error={instancesQuery.error}
        />
        <BatchPlanPanel
          task={activeTask}
          selectedCount={detailPreviewTargets.length}
          batchSize={batchSize}
        />
        <RiskPanel
          task={activeTask}
          failureThreshold={failureThreshold}
          previewTargetCount={detailPreviewTargets.length}
        />
      </div>

      <div className="grid min-h-[24rem] grid-cols-1 gap-3 xl:grid-cols-[minmax(22rem,0.7fr)_minmax(0,1.3fr)]">
        <LogPanel task={activeTask} />
        <Card className="flex min-h-0 flex-col rounded-md py-0 shadow-sm">
          <CardHeader className="h-9 shrink-0 border-b px-3 py-0">
            <CardTitle>{t('fileSync.tableTitle')}</CardTitle>
          </CardHeader>
          <CardContent className="min-h-0 flex-1 p-0">
            <FileSyncTargetDetails
              task={activeTask}
              previewTargets={detailPreviewTargets}
              batchSize={batchSize}
            />
          </CardContent>
        </Card>
      </div>
    </div>
  )
}

function BatchPlanPanel({
  task,
  selectedCount,
  batchSize,
}: {
  task: FileSyncTaskView | null
  selectedCount: number
  batchSize: number
}) {
  const { t } = useTranslation()
  const rows = useMemo(
    () => buildBatchRows(task, selectedCount, batchSize),
    [batchSize, selectedCount, task],
  )
  return (
    <Card className="flex min-h-0 flex-col rounded-md py-0 shadow-sm">
      <CardHeader className="h-9 shrink-0 border-b px-3 py-0">
        <CardTitle>{t('fileSync.batchPlanTitle')}</CardTitle>
      </CardHeader>
      <CardContent className="min-h-0 flex-1 overflow-auto p-0">
        <Table>
          <TableHeader className="sticky top-0 z-10 bg-muted/40">
            <TableRow>
              <TableHead className="h-8 text-xs">{t('fileSync.colBatch')}</TableHead>
              <TableHead className="h-8 text-xs">{t('fileSync.batchRange')}</TableHead>
              <TableHead className="h-8 text-xs">{t('fileSync.batchCount')}</TableHead>
              <TableHead className="h-8 text-xs">{t('fileSync.colStatus')}</TableHead>
              <TableHead className="h-8 text-xs">{t('fileSync.batchProgress')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.length > 0 ? (
              rows.map((row) => (
                <TableRow key={row.batchNo}>
                  <TableCell className="px-2 py-1.5 text-xs">{row.batchNo}</TableCell>
                  <TableCell className="px-2 py-1.5 text-xs">{row.range}</TableCell>
                  <TableCell className="px-2 py-1.5 text-xs">{row.count}</TableCell>
                  <TableCell className="px-2 py-1.5">
                    <StatusPill status={row.status} />
                  </TableCell>
                  <TableCell className="px-2 py-1.5">
                    <ProgressLine value={row.progress} />
                  </TableCell>
                </TableRow>
              ))
            ) : (
              <TableRow>
                <TableCell colSpan={5} className="py-8 text-center text-muted-foreground">
                  {t('fileSync.batchEmpty')}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  )
}

function RiskPanel({
  task,
  failureThreshold,
  previewTargetCount,
}: {
  task: FileSyncTaskView | null
  failureThreshold: number
  previewTargetCount: number
}) {
  const { t } = useTranslation()
  const total = task?.totalTargets ?? previewTargetCount
  const failed = task?.failedTargets ?? 0
  const failureRate = total > 0 ? Math.round((failed / total) * 1000) / 10 : 0
  return (
    <Card className="flex min-h-0 flex-col rounded-md py-0 shadow-sm">
      <CardHeader className="h-9 shrink-0 border-b px-3 py-0">
        <CardTitle>{t('fileSync.riskTitle')}</CardTitle>
      </CardHeader>
      <CardContent className="min-h-0 flex-1 overflow-auto p-3">
        <div className="grid grid-cols-2 gap-2">
          <RiskMetric
            label={t('fileSync.riskFailureRate')}
            value={`${failureRate}%`}
            danger={failureRate >= failureThreshold}
          />
          <RiskMetric label={t('fileSync.riskThreshold')} value={`${failureThreshold}%`} />
          <RiskMetric label={t('fileSync.riskFailedCount')} value={failed} danger={failed > 0} />
          <RiskMetric
            label={t('fileSync.riskHealthy')}
            value={failureRate < failureThreshold ? t('fileSync.riskYes') : t('fileSync.riskNo')}
          />
        </div>
      </CardContent>
    </Card>
  )
}

function LogPanel({ task }: { task: FileSyncTaskView | null }) {
  const { t } = useTranslation()
  const logs = task?.logs ?? []
  return (
    <Card className="flex min-h-0 flex-col rounded-md py-0 shadow-sm">
      <CardHeader className="h-9 shrink-0 border-b px-3 py-0">
        <CardTitle>{t('fileSync.logsTitle')}</CardTitle>
      </CardHeader>
      <CardContent className="min-h-0 flex-1 p-0">
        <div className="h-full min-h-0 overflow-auto bg-zinc-950 p-3 font-mono text-xs text-zinc-100">
          {logs.length === 0 ? (
            <div className="text-zinc-500">{t('fileSync.logsEmpty')}</div>
          ) : (
            logs.map((log, index) => (
              <div key={`${log.createdAt}-${index}`} className="whitespace-pre-wrap">
                <span className={logLevelClass(log.level)}>{log.level}</span>{' '}
                <span className="text-zinc-500">{formatTime(log.createdAt)}</span>{' '}
                {log.serverId && <span className="text-sky-300">{log.serverId}</span>} {log.message}
              </div>
            ))
          )}
        </div>
      </CardContent>
    </Card>
  )
}

function RiskMetric({
  label,
  value,
  danger = false,
}: {
  label: string
  value: ReactNode
  danger?: boolean
}) {
  return (
    <div className="rounded-md bg-secondary px-2 py-1.5">
      <div className="text-[11px] text-muted-foreground">{label}</div>
      <div className={cn('mt-1 text-lg font-semibold leading-none', danger && 'text-destructive')}>
        {value}
      </div>
    </div>
  )
}

function ProgressLine({ value }: { value: number }) {
  return (
    <div className="flex items-center gap-2 text-xs">
      <span className="h-1.5 flex-1 overflow-hidden rounded-full bg-muted">
        <span className="block h-full rounded-full bg-primary" style={{ width: `${value}%` }} />
      </span>
      <span className="w-9 text-right tabular-nums">{value}%</span>
    </div>
  )
}

function SyncToolbar(props: {
  sourceOptions: InstanceView[]
  sourceServerId: string
  directory: string
  batchSize: number
  intervalSec: number
  failureThreshold: number
  busy: boolean
  hasTargets: boolean
  task: FileSyncTaskView | null
  onSourceChange: (value: string) => void
  onDirectoryChange: (value: string) => void
  onBatchSizeChange: (value: number) => void
  onIntervalChange: (value: number) => void
  onFailureThresholdChange: (value: number) => void
  onPlan: () => void
  onTaskAction: (action: TaskAction) => void
}) {
  const { t } = useTranslation()
  return (
    <div className="flex shrink-0 flex-wrap items-end gap-2 rounded-md border bg-background px-3 py-2 shadow-sm xl:flex-nowrap">
      <ToolbarField className="w-64" label={t('fileSync.sourceServer')} id="file-sync-source">
        <Combobox
          id="file-sync-source"
          className="[&_input]:h-8 [&_input]:text-xs"
          value={props.sourceServerId}
          onChange={props.onSourceChange}
          options={props.sourceOptions.map((s) => ({
            value: s.serverId,
            label: `${s.serverId} · ${s.address}`,
          }))}
          allowCustom={false}
          placeholder={
            props.sourceOptions.length === 0
              ? t('fileSync.noSource')
              : t('fileSync.sourceSearchPlaceholder')
          }
        />
      </ToolbarField>
      <ToolbarField className="w-64" label={t('fileSync.directory')} id="file-sync-directory">
        <Input
          id="file-sync-directory"
          className="h-8 text-xs"
          value={props.directory}
          placeholder={t('fileSync.directoryPlaceholder')}
          onChange={(e) => props.onDirectoryChange(e.target.value)}
        />
      </ToolbarField>
      <ToolbarNumberField
        id="file-sync-batch-size"
        label={t('fileSync.batchSize')}
        className="w-24"
        min={1}
        value={props.batchSize}
        onChange={props.onBatchSizeChange}
      />
      <ToolbarNumberField
        id="file-sync-interval"
        label={t('fileSync.intervalSec')}
        className="w-28"
        min={0}
        value={props.intervalSec}
        onChange={props.onIntervalChange}
      />
      <ToolbarNumberField
        id="file-sync-failure"
        label={t('fileSync.failureThreshold')}
        className="w-28"
        min={1}
        max={100}
        value={props.failureThreshold}
        onChange={props.onFailureThresholdChange}
      />
      <div className="ml-auto flex flex-wrap items-center gap-2 xl:flex-nowrap">
        <Button className="h-8" onClick={props.onPlan} disabled={props.busy || !props.hasTargets}>
          <ListChecks className="size-4" />
          {t('fileSync.planTargets')}
        </Button>
        <TaskButton
          label={t('fileSync.start')}
          icon={<Play className="size-4" />}
          disabled={props.busy || !props.task}
          onClick={() => props.onTaskAction('start')}
        />
        <TaskButton
          label={t('fileSync.pause')}
          icon={<Pause className="size-4" />}
          disabled={props.busy || props.task?.status !== 'running'}
          onClick={() => props.onTaskAction('pause')}
        />
        <TaskButton
          label={t('fileSync.resume')}
          icon={<RotateCcw className="size-4" />}
          disabled={props.busy || props.task?.status !== 'paused'}
          onClick={() => props.onTaskAction('resume')}
        />
        <TaskButton
          label={t('fileSync.terminate')}
          icon={<Square className="size-4" />}
          disabled={props.busy || !props.task || isTerminal(props.task.status)}
          onClick={() => props.onTaskAction('terminate')}
          variant="destructive"
        />
      </div>
    </div>
  )
}

function TargetPanel(props: {
  targets: InstanceView[]
  selected: string[]
  onSelectedChange: (value: string[]) => void
  isLoading: boolean
  isError: boolean
  error: unknown
}) {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')
  const [selectedOnly, setSelectedOnly] = useState(false)
  const [pageIndex, setPageIndex] = useState(0)
  const deferredQuery = useDeferredValue(query)
  const filteredTargets = useMemo(
    () => filterInstancesByKeyword(props.targets, deferredQuery),
    [deferredQuery, props.targets],
  )
  const selectedSet = useMemo(() => new Set(props.selected), [props.selected])
  const visibleTargets = useMemo(
    () =>
      selectedOnly
        ? filteredTargets.filter((target) => selectedSet.has(target.serverId))
        : filteredTargets,
    [filteredTargets, selectedOnly, selectedSet],
  )
  const grouped = useMemo(
    () => groupTargets(visibleTargets, t('fileSync.unassignedZone')),
    [t, visibleTargets],
  )
  const selectedTargets = useMemo(() => {
    const targetMap = new Map(props.targets.map((target) => [target.serverId, target]))
    return props.selected
      .map((id) => targetMap.get(id))
      .filter((target): target is InstanceView => Boolean(target))
  }, [props.selected, props.targets])
  const page = getPage(visibleTargets, pageIndex, TARGET_TABLE_PAGE_SIZE)

  useEffect(() => {
    setPageIndex(0)
  }, [deferredQuery, selectedOnly, props.targets.length])

  useEffect(() => {
    if (selectedOnly) setPageIndex(0)
  }, [props.selected.length, selectedOnly])

  const setAll = () =>
    props.onSelectedChange(
      mergeSelectedIds(
        props.selected,
        props.targets.map((target) => target.serverId),
      ),
    )
  const setFiltered = () =>
    props.onSelectedChange(
      mergeSelectedIds(
        props.selected,
        visibleTargets.map((target) => target.serverId),
      ),
    )
  const clearFiltered = () =>
    props.onSelectedChange(
      removeSelectedIds(
        props.selected,
        visibleTargets.map((target) => target.serverId),
      ),
    )
  const clear = () => props.onSelectedChange([])
  const toggle = (id: string) => {
    props.onSelectedChange(
      selectedSet.has(id) ? props.selected.filter((x) => x !== id) : [...props.selected, id],
    )
  }
  const setGroup = (items: InstanceView[], checked: boolean) => {
    const ids = items.map((item) => item.serverId)
    props.onSelectedChange(
      checked ? mergeSelectedIds(props.selected, ids) : removeSelectedIds(props.selected, ids),
    )
  }
  return (
    <Card className="flex min-h-0 flex-col rounded-md py-0 shadow-sm">
      <CardHeader className="h-9 shrink-0 border-b px-3 py-0">
        <CardTitle>{t('fileSync.targetTitle')}</CardTitle>
      </CardHeader>
      <CardContent className="flex min-h-0 flex-1 flex-col gap-2 p-3">
        <div className="flex flex-wrap items-center gap-2">
          <Input
            aria-label={t('fileSync.targetSearchLabel')}
            className="h-8 min-w-0 flex-1 text-xs"
            value={query}
            placeholder={t('fileSync.targetSearchPlaceholder')}
            onChange={(e) => setQuery(e.target.value)}
          />
          <Button
            type="button"
            variant={selectedOnly ? 'default' : 'outline'}
            size="sm"
            onClick={() => setSelectedOnly((value) => !value)}
          >
            {t('fileSync.selectedOnly')}
          </Button>
          <Button type="button" variant="outline" size="sm" onClick={setAll}>
            {t('fileSync.selectAllTargets')}
          </Button>
          <Button type="button" variant="outline" size="sm" onClick={setFiltered}>
            {t('fileSync.selectFilteredTargets')}
          </Button>
          <Button type="button" variant="ghost" size="sm" onClick={clearFiltered}>
            {t('fileSync.clearFilteredTargets')}
          </Button>
          <Button type="button" variant="ghost" size="sm" onClick={clear}>
            {t('fileSync.clearTargets')}
          </Button>
        </div>
        <div className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
          <span>{t('fileSync.targetHint')}</span>
          <span>{t('fileSync.filteredTargets', { count: visibleTargets.length })}</span>
          <span>{t('fileSync.selectedTargetsTotal', { count: props.selected.length })}</span>
        </div>
        <div className="min-h-0 flex-1 overflow-hidden">
          <AsyncSection
            isLoading={props.isLoading}
            isError={props.isError}
            error={props.error}
            skeleton={<TableSkeleton columns={4} rows={3} />}
          >
            {props.targets.length === 0 ? (
              <p className="text-sm text-muted-foreground">{t('fileSync.noTargets')}</p>
            ) : (
              <div className="flex h-full min-h-0 flex-col gap-2">
                <div className="grid max-h-32 shrink-0 gap-1 overflow-auto rounded-md border border-border p-1 md:grid-cols-2 xl:grid-cols-1">
                  {grouped.map((group) => (
                    <GroupTargetStats
                      key={group.key}
                      group={group}
                      selectedSet={selectedSet}
                      onGroupChange={setGroup}
                    />
                  ))}
                </div>
                <SelectedTargetSummary
                  targets={selectedTargets}
                  onRemove={(serverId) =>
                    props.onSelectedChange(props.selected.filter((id) => id !== serverId))
                  }
                />
                <TargetSelectionTable
                  page={page}
                  selectedSet={selectedSet}
                  onToggle={toggle}
                  onPageChange={setPageIndex}
                />
              </div>
            )}
          </AsyncSection>
        </div>
      </CardContent>
    </Card>
  )
}

function SelectedTargetSummary({
  targets,
  onRemove,
}: {
  targets: InstanceView[]
  onRemove: (serverId: string) => void
}) {
  const { t } = useTranslation()
  if (targets.length === 0) return null
  const visible = targets.slice(0, SELECTED_SUMMARY_LIMIT)
  const hidden = Math.max(0, targets.length - visible.length)
  return (
    <div className="flex shrink-0 items-center gap-2 overflow-hidden rounded-md border border-dashed border-border bg-muted/20 px-2 py-1">
      <div className="flex shrink-0 items-center gap-2 text-sm">
        <span className="font-medium">{t('fileSync.selectedSummaryTitle')}</span>
        <span className="text-muted-foreground">
          {t('fileSync.selectedTargets', { count: targets.length })}
        </span>
      </div>
      <div className="flex min-w-0 flex-1 gap-1 overflow-x-auto">
        {visible.map((target) => (
          <span
            key={target.serverId}
            className="inline-flex shrink-0 items-center gap-1 rounded border border-border bg-background px-1.5 py-0.5 text-xs"
          >
            <span className="font-mono">{target.serverId}</span>
            <button
              type="button"
              className="rounded px-1 text-muted-foreground hover:bg-muted hover:text-foreground"
              aria-label={t('fileSync.removeSelectedTarget', { serverId: target.serverId })}
              onClick={() => onRemove(target.serverId)}
            >
              {t('fileSync.removeSelectedShort')}
            </button>
          </span>
        ))}
        {hidden > 0 && (
          <span className="inline-flex shrink-0 items-center rounded border border-dashed px-1.5 py-0.5 text-xs text-muted-foreground">
            {t('fileSync.selectedHidden', { count: hidden })}
          </span>
        )}
      </div>
    </div>
  )
}

function GroupTargetStats({
  group,
  selectedSet,
  onGroupChange,
}: {
  group: { key: string; items: InstanceView[] }
  selectedSet: Set<string>
  onGroupChange: (items: InstanceView[], checked: boolean) => void
}) {
  const { t } = useTranslation()
  const selectedCount = group.items.filter((item) => selectedSet.has(item.serverId)).length
  const allSelected = selectedCount === group.items.length
  return (
    <label
      className={cn(
        'flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 hover:bg-muted',
        allSelected && 'bg-primary/5 text-primary',
      )}
    >
      <input
        type="checkbox"
        className="size-4 shrink-0"
        aria-label={`${t('fileSync.selectGroup')} ${group.key}`}
        checked={allSelected}
        onChange={(e) => onGroupChange(group.items, e.currentTarget.checked)}
      />
      <span className="min-w-0 flex-1 truncate text-xs font-medium">{group.key}</span>
      <span className="shrink-0 text-xs text-muted-foreground">
        {selectedCount} / {group.items.length}
      </span>
    </label>
  )
}

function TargetSelectionTable({
  page,
  selectedSet,
  onToggle,
  onPageChange,
}: {
  page: PageResult<InstanceView>
  selectedSet: Set<string>
  onToggle: (serverId: string) => void
  onPageChange: (pageIndex: number) => void
}) {
  const { t } = useTranslation()
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2">
      <div className="min-h-0 flex-1 overflow-auto">
        <Table aria-label={t('fileSync.targetTableLabel')}>
          <TableHeader className="sticky top-0 z-10 bg-muted/40">
            <TableRow>
              <TableHead className="h-8 w-12 text-xs">{t('fileSync.colSelect')}</TableHead>
              <TableHead className="h-8 text-xs">{t('fileSync.colServer')}</TableHead>
              <TableHead className="h-8 text-xs">{t('fileSync.colGroup')}</TableHead>
              <TableHead className="h-8 text-xs">{t('fileSync.colZone')}</TableHead>
              <TableHead className="h-8 text-xs">{t('fileSync.colAddress')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {page.items.length > 0 ? (
              page.items.map((server) => (
                <TableRow key={server.serverId}>
                  <TableCell className="px-2 py-1.5">
                    <input
                      type="checkbox"
                      className="size-4"
                      aria-label={t('fileSync.targetRowCheckbox', { serverId: server.serverId })}
                      checked={selectedSet.has(server.serverId)}
                      onChange={() => onToggle(server.serverId)}
                    />
                  </TableCell>
                  <TableCell className="px-2 py-1.5 font-mono text-xs">{server.serverId}</TableCell>
                  <TableCell className="px-2 py-1.5 text-xs">{server.group}</TableCell>
                  <TableCell className="px-2 py-1.5 text-xs">{server.zone || '-'}</TableCell>
                  <TableCell className="px-2 py-1.5 font-mono text-xs">{server.address}</TableCell>
                </TableRow>
              ))
            ) : (
              <TableRow>
                <TableCell colSpan={5} className="text-center text-muted-foreground">
                  {t('fileSync.noTargetsInFilter')}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>
      <PaginationBar
        page={page}
        ariaLabel={t('fileSync.targetPagerLabel')}
        onPageChange={onPageChange}
      />
    </div>
  )
}

function FileSyncTargetDetails({
  task,
  previewTargets,
  batchSize,
}: {
  task: FileSyncTaskView | null
  previewTargets: InstanceView[]
  batchSize: number
}) {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')
  const [status, setStatus] = useState<FileSyncTargetStatus | 'all'>('all')
  const [failedFirst, setFailedFirst] = useState(false)
  const [pageIndex, setPageIndex] = useState(0)
  const rows = useMemo(
    () => task?.targets ?? buildPreviewTargetRows(previewTargets, batchSize),
    [batchSize, previewTargets, task?.targets],
  )
  const filteredRows = useMemo(
    () => filterFileSyncTargets(rows, { keyword: query, status, failedFirst }),
    [failedFirst, query, rows, status],
  )
  const page = getPage(filteredRows, pageIndex, FILE_SYNC_TABLE_PAGE_SIZE)
  const emptyText =
    rows.length > 0 ? t('fileSync.noDetailTargetsInFilter') : t('fileSync.emptyTable')

  useEffect(() => {
    setPageIndex(0)
  }, [failedFirst, query, rows.length, status])

  return (
    <div className="flex h-full min-h-0 flex-col gap-2 p-3">
      <div className="flex flex-wrap items-center gap-2">
        <Input
          aria-label={t('fileSync.detailSearchLabel')}
          className="min-w-56 flex-1"
          value={query}
          placeholder={t('fileSync.detailSearchPlaceholder')}
          onChange={(e) => setQuery(e.target.value)}
        />
        <select
          aria-label={t('fileSync.detailStatusLabel')}
          value={status}
          onChange={(e) => setStatus(e.target.value as FileSyncTargetStatus | 'all')}
          className="h-8 rounded-lg border border-input bg-background px-2 text-sm"
        >
          <option value="all">{t('fileSync.detailStatusAll')}</option>
          {ALL_TARGET_STATUSES.map((item) => (
            <option key={item} value={item}>
              {t(`fileSync.status.${item}`, { defaultValue: item })}
            </option>
          ))}
        </select>
        <Button
          type="button"
          variant={failedFirst ? 'default' : 'outline'}
          size="sm"
          onClick={() => setFailedFirst((value) => !value)}
        >
          {t('fileSync.failedFirst')}
        </Button>
        <span className="text-sm text-muted-foreground">
          {t('fileSync.detailFilteredTargets', { count: filteredRows.length })}
        </span>
      </div>
      <div className="min-h-0 flex-1 overflow-auto">
        <Table aria-label={t('fileSync.detailTableLabel')}>
          <TableHeader className="sticky top-0 z-10 bg-muted/40">
            <TableRow>
              <TableHead className="h-8 text-xs">{t('fileSync.colBatch')}</TableHead>
              <TableHead className="h-8 text-xs">{t('fileSync.colServer')}</TableHead>
              <TableHead className="h-8 text-xs">{t('fileSync.colGroup')}</TableHead>
              <TableHead className="h-8 text-xs">{t('fileSync.colZone')}</TableHead>
              <TableHead className="h-8 text-xs">{t('fileSync.colStatus')}</TableHead>
              <TableHead className="h-8 text-xs">{t('fileSync.colChanged')}</TableHead>
              <TableHead className="h-8 text-xs">{t('fileSync.colSkipped')}</TableHead>
              <TableHead className="h-8 text-xs">{t('fileSync.colBytes')}</TableHead>
              <TableHead className="h-8 text-xs">{t('fileSync.colError')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {page.items.length > 0 ? (
              page.items.map((row) => (
                <TableRow key={`${row.taskId}/${row.serverId}`}>
                  <TableCell className="px-2 py-1.5 text-xs">{row.batchNo}</TableCell>
                  <TableCell className="px-2 py-1.5 font-mono text-xs">{row.serverId}</TableCell>
                  <TableCell className="px-2 py-1.5 text-xs">{row.group}</TableCell>
                  <TableCell className="px-2 py-1.5 text-xs">{row.zone || '-'}</TableCell>
                  <TableCell className="px-2 py-1.5">
                    <StatusPill status={row.status} />
                  </TableCell>
                  <TableCell className="px-2 py-1.5 text-xs">{row.changedFileCount}</TableCell>
                  <TableCell className="px-2 py-1.5 text-xs">{row.skippedFileCount}</TableCell>
                  <TableCell className="px-2 py-1.5 text-xs">
                    {formatBytes(row.bytesDone)} / {formatBytes(row.bytesTotal)}
                  </TableCell>
                  <TableCell className="max-w-xs truncate px-2 py-1.5 text-xs">
                    {row.error || '-'}
                  </TableCell>
                </TableRow>
              ))
            ) : (
              <TableRow>
                <TableCell colSpan={9} className="text-center text-muted-foreground">
                  {emptyText}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>
      <PaginationBar
        page={page}
        ariaLabel={t('fileSync.detailPagerLabel')}
        onPageChange={setPageIndex}
      />
    </div>
  )
}

interface PageResult<T> {
  items: T[]
  total: number
  pageIndex: number
  pageCount: number
  pageSize: number
}

function getPage<T>(items: T[], pageIndex: number, pageSize: number): PageResult<T> {
  const safePageSize = Math.max(1, pageSize)
  const pageCount = Math.max(1, Math.ceil(items.length / safePageSize))
  const currentPage = Math.min(Math.max(0, pageIndex), pageCount - 1)
  const start = currentPage * safePageSize
  return {
    items: items.slice(start, start + safePageSize),
    total: items.length,
    pageIndex: currentPage,
    pageCount,
    pageSize: safePageSize,
  }
}

function PaginationBar({
  page,
  ariaLabel,
  onPageChange,
}: {
  page: PageResult<unknown>
  ariaLabel: string
  onPageChange: (pageIndex: number) => void
}) {
  const { t } = useTranslation()
  if (page.total <= page.pageSize) return null
  return (
    <nav
      aria-label={ariaLabel}
      className="flex flex-wrap items-center justify-end gap-2 py-2 text-sm text-muted-foreground"
    >
      <span>
        {t('fileSync.pageStatus', {
          page: page.pageIndex + 1,
          pageCount: page.pageCount,
          count: page.total,
        })}
      </span>
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={page.pageIndex === 0}
        onClick={() => onPageChange(page.pageIndex - 1)}
      >
        {t('fileSync.prevPage')}
      </Button>
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={page.pageIndex >= page.pageCount - 1}
        onClick={() => onPageChange(page.pageIndex + 1)}
      >
        {t('fileSync.nextPage')}
      </Button>
    </nav>
  )
}

function ToolbarField({
  id,
  label,
  children,
  className,
}: {
  id: string
  label: string
  children: ReactNode
  className?: string
}) {
  return (
    <div className={cn('min-w-0 space-y-1', className)}>
      <Label htmlFor={id} className="text-[11px] leading-none text-muted-foreground">
        {label}
      </Label>
      {children}
    </div>
  )
}

function ToolbarNumberField(props: {
  id: string
  label: string
  value: number
  min: number
  max?: number
  className?: string
  onChange: (value: number) => void
}) {
  return (
    <ToolbarField id={props.id} label={props.label} className={props.className}>
      <Input
        id={props.id}
        className="h-8 text-xs"
        type="number"
        min={props.min}
        max={props.max}
        value={props.value}
        onChange={(e) => props.onChange(Number(e.target.value))}
      />
    </ToolbarField>
  )
}

interface BatchRow {
  batchNo: number
  range: string
  count: number
  status: string
  progress: number
}

function buildBatchRows(
  task: FileSyncTaskView | null,
  selectedCount: number,
  batchSize: number,
): BatchRow[] {
  if (task?.targets.length) return buildTaskBatchRows(task.targets)
  if (selectedCount <= 0) return []
  return buildDraftBatchRows(selectedCount, batchSize)
}

function buildTaskBatchRows(targets: FileSyncTargetView[]): BatchRow[] {
  const byBatch = new Map<number, FileSyncTargetView[]>()
  for (const target of targets) {
    const list = byBatch.get(target.batchNo) ?? []
    list.push(target)
    byBatch.set(target.batchNo, list)
  }
  return [...byBatch.entries()]
    .sort(([a], [b]) => a - b)
    .map(([batchNo, list]) => ({
      batchNo,
      range: `${list[0]?.serverId ?? '-'} - ${list[list.length - 1]?.serverId ?? '-'}`,
      count: list.length,
      status: batchStatus(list),
      progress: batchProgress(list),
    }))
}

function buildDraftBatchRows(selectedCount: number, batchSize: number): BatchRow[] {
  const size = Math.max(1, batchSize)
  const total = Math.ceil(selectedCount / size)
  return Array.from({ length: total }, (_, index) => {
    const start = index * size + 1
    const end = Math.min(selectedCount, (index + 1) * size)
    return {
      batchNo: index + 1,
      range: `${start}-${end}`,
      count: end - start + 1,
      status: 'pending',
      progress: 0,
    }
  })
}

function buildPreviewTargetRows(targets: InstanceView[], batchSize: number): FileSyncTargetView[] {
  const size = Math.max(1, batchSize)
  return targets.map((target, index) => ({
    taskId: 'preview',
    batchNo: Math.floor(index / size) + 1,
    serverId: target.serverId,
    namespace: target.namespace,
    group: target.group,
    zone: target.zone ?? '',
    status: 'pending',
    backupPath: '',
    currentFileCount: 128,
    changedFileCount: 0,
    skippedFileCount: 0,
    bytesTotal: 96 * 1024 * 1024,
    bytesDone: 0,
    error: '',
    updatedAt: target.lastHeartbeat,
  }))
}

function batchStatus(list: FileSyncTargetView[]): string {
  if (list.some((target) => target.status === 'failed')) return 'failed'
  if (list.every((target) => ['succeeded', 'skipped'].includes(target.status))) return 'succeeded'
  if (
    list.some((target) =>
      ['manifesting', 'backing-up', 'transferring', 'applying'].includes(target.status),
    )
  ) {
    return 'transferring'
  }
  return 'pending'
}

function batchProgress(list: FileSyncTargetView[]): number {
  if (list.length === 0) return 0
  const done = list.filter((target) =>
    ['succeeded', 'failed', 'skipped'].includes(target.status),
  ).length
  return Math.round((done / list.length) * 100)
}

function TaskButton(props: {
  label: string
  icon: ReactNode
  disabled: boolean
  onClick: () => void
  variant?: 'outline' | 'destructive'
}) {
  return (
    <Button
      className="h-8"
      variant={props.variant ?? 'outline'}
      onClick={props.onClick}
      disabled={props.disabled}
    >
      {props.icon}
      {props.label}
    </Button>
  )
}

function ErrorBanner({ message }: { message: string }) {
  const { t } = useTranslation()
  return (
    <div className="flex items-start gap-2 rounded-lg border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive">
      <AlertTriangle className="mt-0.5 size-4 shrink-0" />
      <div>
        <div className="font-medium">{t('fileSync.errorTitle')}</div>
        <div>{message}</div>
      </div>
    </div>
  )
}

function buildSummary(
  t: TFunction,
  task: FileSyncTaskView | null,
  previewTargetCount: number,
  batchSize: number,
): SummaryItem[] {
  const total = task?.totalTargets ?? previewTargetCount
  const done =
    (task?.succeededTargets ?? 0) + (task?.failedTargets ?? 0) + (task?.skippedTargets ?? 0)
  const successRate =
    total > 0 ? Math.round(((task?.succeededTargets ?? 0) / total) * 1000) / 10 : 0
  const batchProgress =
    task && task.totalBatches > 0
      ? `${task.currentBatch} / ${task.totalBatches}`
      : `0 / ${Math.ceil(total / Math.max(1, batchSize))}`
  return [
    {
      label: t('fileSync.summaryStatus'),
      value: task ? t(`fileSync.status.${task.status}`, { defaultValue: task.status }) : t('fileSync.previewStatus'),
    },
    { label: t('fileSync.summaryTargets'), value: total },
    { label: t('fileSync.summarySucceeded'), value: task?.succeededTargets ?? 0, tone: 'success' },
    { label: t('fileSync.summaryProcessing'), value: Math.max(0, total - done), tone: 'warning' },
    {
      label: t('fileSync.summaryFailed'),
      value: task?.failedTargets ?? 0,
      tone: (task?.failedTargets ?? 0) > 0 ? 'danger' : 'default',
    },
    {
      label: t('fileSync.summaryChanged'),
      value: task?.targets.reduce((sum, target) => sum + target.changedFileCount, 0) ?? 0,
    },
    {
      label: t('fileSync.summarySkipped'),
      value: task?.targets.reduce((sum, target) => sum + target.skippedFileCount, 0) ?? 0,
    },
    {
      label: t('fileSync.summarySuccessRate'),
      value: `${successRate}%`,
      tone: successRate >= 95 ? 'success' : 'warning',
    },
    {
      label: t('fileSync.summaryBatch'),
      value: batchProgress,
    },
    {
      label: t('fileSync.summaryWorkers'),
      value: Math.min(task?.batchSize ?? batchSize, Math.max(1, total)),
    },
  ]
}

function StatusPill({ status }: { status: string }) {
  const { t } = useTranslation()
  return (
    <span
      className={cn(
        'inline-flex rounded px-1.5 py-0.5 text-xs',
        status === 'failed' && 'bg-destructive/10 text-destructive',
        status === 'succeeded' && 'bg-green-500/10 text-green-700',
        ['pending', 'manifesting', 'backing-up', 'transferring', 'applying'].includes(status) &&
          'bg-amber-500/10 text-amber-700',
      )}
    >
      {t(`fileSync.status.${status}`, { defaultValue: status })}
    </span>
  )
}

function groupTargets(
  targets: InstanceView[],
  unassignedLabel: string,
): Array<{ key: string; items: InstanceView[] }> {
  const map = new Map<string, InstanceView[]>()
  for (const target of targets) {
    const key = `${target.group} / ${target.zone || unassignedLabel}`
    const items = map.get(key)
    if (items) items.push(target)
    else map.set(key, [target])
  }
  return [...map.entries()]
    .map(([key, items]) => ({
      key,
      items: [...items].sort((a, b) => a.serverId.localeCompare(b.serverId)),
    }))
    .sort((a, b) => a.key.localeCompare(b.key))
}

function runTaskAction(id: string, action: TaskAction): Promise<FileSyncTaskView> {
  if (action === 'start') return startFileSyncTask(id)
  if (action === 'pause') return pauseFileSyncTask(id)
  if (action === 'resume') return resumeFileSyncTask(id)
  return terminateFileSyncTask(id)
}

function isTerminal(status: string): boolean {
  return ['succeeded', 'failed', 'terminated', 'circuit-broken'].includes(status)
}

function applyEvent(
  setTask: Dispatch<SetStateAction<FileSyncTaskView | null>>,
  event: FileSyncEvent,
) {
  setTask((prev) => {
    if (!prev) return prev
    const raw = event as FileSyncEvent & {
      status?: FileSyncTaskView['status']
      logId?: number
      batchId?: number
      level?: string
      serverId?: string
      message?: string
      createdAt?: string
    }
    if (event.type === 'task' && (event.task || raw.status)) {
      return { ...prev, ...event.task, ...(raw.status ? { status: raw.status } : {}) }
    }
    if (event.type === 'target' && event.target?.serverId) return mergeTarget(prev, event.target)
    if (event.type === 'log' && (event.log || raw.message))
      return appendFileSyncLog(
        prev,
        normalizeLog(
          prev.id,
          event.log ?? {
            id: raw.logId,
            serverId: raw.serverId,
            level: raw.level,
            message: raw.message,
            createdAt: raw.createdAt,
          },
        ),
      )
    if (event.type === 'error' && event.message) return { ...prev, lastError: event.message }
    return prev
  })
}

function appendFileSyncLog(
  task: FileSyncTaskView,
  log: FileSyncTaskView['logs'][number],
): FileSyncTaskView {
  if (log.id && task.logs.some((item) => item.id === log.id)) return task
  return { ...task, logs: [...task.logs, log] }
}

function lastFileSyncLogId(task: FileSyncTaskView | null): number {
  return Math.max(0, ...(task?.logs ?? []).map((log) => log.id ?? 0))
}

function mergeTarget(task: FileSyncTaskView, patch: Partial<FileSyncTargetView>): FileSyncTaskView {
  return {
    ...task,
    targets: task.targets.map((target) =>
      target.serverId === patch.serverId ? { ...target, ...patch } : target,
    ),
  }
}

function normalizeLog(
  taskId: string,
  log: Omit<Partial<FileSyncTaskView['logs'][number]>, 'level'> & { level?: string },
) {
  return {
    id: log.id,
    taskId,
    batchNo: log.batchNo ?? 0,
    serverId: log.serverId ?? '',
    level: normalizeLogLevel(log.level),
    message: log.message ?? '',
    createdAt: log.createdAt ?? new Date().toISOString(),
  }
}

function normalizeLogLevel(level: string | undefined): FileSyncTaskView['logs'][number]['level'] {
  const upper = (level ?? 'INFO').toUpperCase()
  if (upper === 'WARN' || upper === 'ERROR' || upper === 'DEBUG') return upper
  return 'INFO'
}

function logLevelClass(level: string): string {
  if (level === 'ERROR') return 'text-red-400'
  if (level === 'WARN') return 'text-amber-300'
  if (level === 'DEBUG') return 'text-zinc-400'
  return 'text-emerald-300'
}
