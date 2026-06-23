// 反向抓取任务历史列表（FR-60）：按状态筛选 + 状态 Badge + 进度（选定/总数）+ 取消 / 查看。
// 选中某任务后由上层据其 status 切换审核台 / 冲突台 / 只读状态面板。

import { useTranslation } from 'react-i18next'
import { useMutation, useQueryClient } from '@tanstack/react-query'

import { cancelReverseFetchTask } from '../../api/client'
import type { ReverseFetchTaskView } from '../../api/types'
import { useMessage } from '../../components/useMessage'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

// 终态集合：终态任务不可取消。
const TERMINAL = new Set(['done', 'failed', 'cancelled', 'expired'])

// 状态 → i18n key 映射（集中消枚举判断，避免散落 if-else）。
const STATUS_LABEL_KEY: Record<string, string> = {
  scanning: 'reverseFetchTask.statusScanning',
  'pending-review': 'reverseFetchTask.statusPendingReview',
  fetching: 'reverseFetchTask.statusFetching',
  'conflict-review': 'reverseFetchTask.statusConflictReview',
  ingesting: 'reverseFetchTask.statusIngesting',
  done: 'reverseFetchTask.statusDone',
  failed: 'reverseFetchTask.statusFailed',
  cancelled: 'reverseFetchTask.statusCancelled',
  expired: 'reverseFetchTask.statusExpired',
}

// 状态 → Badge variant：待人工态用 default（醒目）、失败/过期用 destructive、进行中用 secondary、其余 outline。
function statusVariant(status: string): 'default' | 'secondary' | 'destructive' | 'outline' {
  if (status === 'pending-review' || status === 'conflict-review') return 'default'
  if (status === 'failed' || status === 'expired') return 'destructive'
  if (status === 'scanning' || status === 'fetching' || status === 'ingesting') return 'secondary'
  return 'outline'
}

// 状态 Badge（供本列表与详情面板复用）。
export function TaskStatusBadge({ status }: { status: string }) {
  const { t } = useTranslation()
  const key = STATUS_LABEL_KEY[status]
  return (
    <Badge variant={statusVariant(status)} className="text-xs">
      {key ? t(key) : status}
    </Badge>
  )
}

export default function ReverseFetchTaskList({
  tasks,
  selectedId,
  onSelect,
}: {
  tasks: ReverseFetchTaskView[]
  selectedId: number | null
  onSelect: (task: ReverseFetchTaskView) => void
}) {
  const { t } = useTranslation()
  const msg = useMessage()
  const qc = useQueryClient()

  const cancelMut = useMutation({
    mutationFn: (id: number) => cancelReverseFetchTask(id),
    onSuccess: (task) => {
      msg.showSuccess(t('reverseFetchTask.msgCancelled', { id: task.id }))
      qc.invalidateQueries({ queryKey: ['reverse-fetch-tasks'] })
      qc.invalidateQueries({ queryKey: ['reverse-fetch-task', task.id] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  if (tasks.length === 0) {
    return (
      <div className="rounded-lg border border-dashed border-border px-3 py-6 text-center text-sm text-muted-foreground">
        {t('reverseFetchTask.emptyTasks')}
      </div>
    )
  }

  return (
    <div className="rounded-lg border border-border bg-card overflow-hidden">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-16">{t('reverseFetchTask.colId')}</TableHead>
            <TableHead>{t('reverseFetchTask.colServer')}</TableHead>
            <TableHead>{t('reverseFetchTask.colScope')}</TableHead>
            <TableHead>{t('reverseFetchTask.colStatus')}</TableHead>
            <TableHead>{t('reverseFetchTask.colProgress')}</TableHead>
            <TableHead>{t('reverseFetchTask.colCreatedAt')}</TableHead>
            <TableHead className="text-right">{t('reverseFetchTask.colActions')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {tasks.map((task) => (
            <TableRow
              key={task.id}
              data-selected={task.id === selectedId}
              className="data-[selected=true]:bg-muted/50"
            >
              <TableCell className="font-mono text-xs">#{task.id}</TableCell>
              <TableCell className="font-mono text-xs break-all">{task.serverId}</TableCell>
              <TableCell className="text-xs">
                {task.scope === 'server'
                  ? `${t('reverseFetchTask.scopeServer')} · ${task.target || '-'}`
                  : `${t('reverseFetchTask.scopeGroup')} · ${task.group}`}
              </TableCell>
              <TableCell>
                <TaskStatusBadge status={task.status} />
              </TableCell>
              <TableCell className="font-mono text-xs">
                {t('reverseFetchTask.progressFiles', {
                  selected: task.selectedCount,
                  total: task.totalFiles,
                })}
              </TableCell>
              <TableCell className="text-xs text-muted-foreground">{task.createdAt}</TableCell>
              <TableCell className="text-right">
                <div className="flex justify-end gap-1.5">
                  <Button size="xs" variant="outline" onClick={() => onSelect(task)}>
                    {t('reverseFetchTask.viewBtn')}
                  </Button>
                  {!TERMINAL.has(task.status) && (
                    <Button
                      size="xs"
                      variant="destructive"
                      disabled={cancelMut.isPending}
                      onClick={() => cancelMut.mutate(task.id)}
                    >
                      {cancelMut.isPending
                        ? t('reverseFetchTask.cancelling')
                        : t('reverseFetchTask.cancelBtn')}
                    </Button>
                  )}
                </div>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}
