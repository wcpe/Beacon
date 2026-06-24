// 反向抓取审核台 + 任务台主页（FR-60，路由 /reverse-fetch）：
// 任务台（建扫描任务 + 历史列表 + 状态/进度/取消）+ 按选中任务 status 切面板
// （pending-review→审核台、conflict-review→冲突台、其余→只读状态）+ 忽略规则面板。
// 选中任务每 2s 轮询刷新至待人工态 / 终态（复用 ImprintPage 2s 轮询范式）。

import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'

import {
  getReverseFetchTask,
  listInstances,
  listReverseFetchTasks,
  zoneSummary,
} from '../../api/client'
import type { ReverseFetchScope, ReverseFetchTaskView } from '../../api/types'
import { Badge } from '@/components/ui/badge'
import ReverseFetchTaskTrigger from './ReverseFetchTaskTrigger'
import ReverseFetchTaskList, { TaskStatusBadge } from './ReverseFetchTaskList'
import ReverseFetchReviewPanel from './ReverseFetchReviewPanel'
import ReverseFetchConflictPanel from './ReverseFetchConflictPanel'
import ReverseFetchIgnoreRulePanel from './ReverseFetchIgnoreRulePanel'

// 任务详情轮询的终止状态（待人工态与终态均停轮询）。
const POLL_STOP = new Set([
  'pending-review',
  'conflict-review',
  'done',
  'failed',
  'cancelled',
  'expired',
])

// 状态分组：只读提示用（doneHint/failedHint/...）。
const READONLY_HINT_KEY: Record<string, string> = {
  done: 'reverseFetchTask.doneHint',
  failed: 'reverseFetchTask.failedHint',
  cancelled: 'reverseFetchTask.cancelledHint',
  expired: 'reverseFetchTask.expiredHint',
}

export default function ReverseFetchTaskPage() {
  const { t } = useTranslation()
  // 当前选中任务 id（轮询其详情；据 status 切面板）
  const [selectedId, setSelectedId] = useState<number | null>(null)
  // 任务历史状态筛选
  const [statusFilter, setStatusFilter] = useState('')

  const instancesQuery = useQuery({
    queryKey: ['instances-all'],
    queryFn: () => listInstances({}),
  })
  const zonesQuery = useQuery({ queryKey: ['zones-summary'], queryFn: () => zoneSummary() })

  // 大区候选（zone 汇总 + 实例派生的并集，作为入库目标组）。
  const groupOptions = useMemo(() => {
    const set = new Set<string>()
    for (const z of zonesQuery.data ?? []) if (z.group) set.add(z.group)
    for (const i of instancesQuery.data ?? []) if (i.group) set.add(i.group)
    return Array.from(set).sort()
  }, [zonesQuery.data, instancesQuery.data])

  // 任务历史列表（按状态筛选；建/取消/提交后由各面板失效缓存触发刷新）。
  const tasksQuery = useQuery({
    queryKey: ['reverse-fetch-tasks', statusFilter],
    queryFn: () => listReverseFetchTasks({ status: statusFilter || undefined }),
    refetchInterval: 5000,
  })

  // 选中任务详情：每 2s 轮询至待人工态 / 终态。
  const taskQuery = useQuery({
    queryKey: ['reverse-fetch-task', selectedId],
    queryFn: () => getReverseFetchTask(selectedId!),
    enabled: selectedId != null,
    refetchInterval: (q) => {
      const s = q.state.data?.status
      if (s && POLL_STOP.has(s)) return false
      return 2000
    },
  })
  const task = taskQuery.data

  return (
    <div className="flex flex-col h-full overflow-hidden gap-3">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">{t('reverseFetchTask.title')}</h1>
        <Badge variant="outline" className="text-xs">
          {t('reverseFetchTask.badge')}
        </Badge>
      </div>

      {/* 建扫描任务 */}
      <ReverseFetchTaskTrigger
        instances={instancesQuery.data ?? []}
        groups={groupOptions}
        onCreated={(created) => setSelectedId(created.id)}
      />

      {/* 任务历史 + 状态筛选 */}
      <div className="flex items-center gap-3">
        <h2 className="text-sm font-medium">{t('reverseFetchTask.listTitle')}</h2>
        <select
          aria-label={t('reverseFetchTask.filterStatus')}
          className="h-8 w-36 rounded border border-input bg-background px-2 text-sm"
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value)}
        >
          <option value="">{t('reverseFetchTask.statusAll')}</option>
          <option value="scanning">{t('reverseFetchTask.statusScanning')}</option>
          <option value="pending-review">{t('reverseFetchTask.statusPendingReview')}</option>
          <option value="fetching">{t('reverseFetchTask.statusFetching')}</option>
          <option value="conflict-review">{t('reverseFetchTask.statusConflictReview')}</option>
          <option value="ingesting">{t('reverseFetchTask.statusIngesting')}</option>
          <option value="done">{t('reverseFetchTask.statusDone')}</option>
          <option value="failed">{t('reverseFetchTask.statusFailed')}</option>
          <option value="cancelled">{t('reverseFetchTask.statusCancelled')}</option>
          <option value="expired">{t('reverseFetchTask.statusExpired')}</option>
        </select>
        {selectedId != null && (
          <Badge variant="secondary" className="text-xs">
            {t('reverseFetchTask.selectedTaskBadge', { id: selectedId })}
          </Badge>
        )}
      </div>

      <ReverseFetchTaskList
        tasks={tasksQuery.data ?? []}
        selectedId={selectedId}
        onSelect={(s) => setSelectedId(s.id)}
      />

      {/* 选中任务详情面板（据 status 切换） */}
      {selectedId == null ? (
        <div className="flex flex-1 items-center justify-center rounded-lg border border-dashed border-border text-sm text-muted-foreground">
          {t('reverseFetchTask.emptyHint')}
        </div>
      ) : !task ? (
        <div className="flex flex-1 items-center justify-center rounded-lg border border-border text-sm text-muted-foreground">
          {t('common.loading')}
        </div>
      ) : (
        <TaskDetail task={task} onChanged={() => taskQuery.refetch()} />
      )}
    </div>
  )
}

// 任务详情：据 status 切审核台 / 冲突台 / 只读状态 + 始终展示该作用域忽略规则面板。
function TaskDetail({
  task,
  onChanged,
}: {
  task: ReverseFetchTaskView
  onChanged: () => void
}) {
  const { t } = useTranslation()

  let body: React.ReactNode
  if (task.status === 'pending-review') {
    body = <ReverseFetchReviewPanel task={task} onSubmitted={onChanged} />
  } else if (task.status === 'conflict-review') {
    body = <ReverseFetchConflictPanel taskId={task.id} onResolved={onChanged} />
  } else {
    const hintKey = READONLY_HINT_KEY[task.status]
    body = (
      <div className="flex flex-1 flex-col items-center justify-center gap-2 text-sm text-muted-foreground">
        <div className="flex items-center gap-2">
          <TaskStatusBadge status={task.status} />
          <span>
            {hintKey
              ? t(hintKey)
              : t('reverseFetchTask.waiting', { serverId: task.serverId, status: task.status })}
          </span>
        </div>
        {/* 失败原因明细（FR-87）：failed 任务回传的 agent 错误，让运维免翻磁盘日志即可定位 */}
        {task.status === 'failed' && task.lastError && (
          <div className="max-w-2xl rounded border border-destructive/40 bg-destructive/5 px-3 py-2 text-xs text-destructive">
            <span className="font-medium">{t('reverseFetchTask.lastErrorLabel')}：</span>
            <span className="break-all">{task.lastError}</span>
          </div>
        )}
      </div>
    )
  }

  return (
    <div className="flex flex-1 min-h-0 flex-col gap-3">
      <div className="flex flex-1 min-h-0 rounded-lg border border-border bg-card overflow-hidden">
        {body}
      </div>
      <ReverseFetchIgnoreRulePanel
        namespace={task.namespace}
        scope={task.scope as ReverseFetchScope}
        group={task.group}
        target={task.target}
      />
    </div>
  )
}
