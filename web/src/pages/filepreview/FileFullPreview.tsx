// 文件树全量预览（FR-68）：选在线 bukkit 源 → 触发扫描（复用 FR-58 受管任务 scan）拿全量磁盘清单 →
// 与 FR-45 有效树交叉比对 → 列全量文件、Badge 追踪/未追踪；追踪文件可点开看合并结果（逐键来源），
// 未追踪仅列 path/size（附「去反向抓取纳管」链接）。预览只读：读完清单即 cancelReverseFetchTask，
// 不进入 ingest（不提交选定），避免遗留 pending 任务。
//
// 数据流：createScanTask → 2s 轮询 getReverseFetchTask 至 pending-review/failed → 读 task.files（全量）；
// 并行 effectiveFiles 取追踪 path 集；二者交叉得每文件 tracked 标记。

import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { useMutation, useQuery } from '@tanstack/react-query'

import {
  cancelReverseFetchTask,
  createScanTask,
  effectiveFiles,
  getReverseFetchTask,
} from '../../api/client'
import type { EffectiveFileItem } from '../../api/client'
import type { InstanceView, ReverseFetchScanFileView } from '../../api/types'
import { useMessage } from '../../components/useMessage'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ScrollArea } from '@/components/ui/scroll-area'
import FileMergeCard from './FileMergeCard'

// 全量清单轮询的终止状态：清单已到（pending-review）或扫描失败/终态。
const SCAN_DONE = new Set(['pending-review', 'failed', 'cancelled', 'expired'])

// 字节数转人类可读（B/KB/MB）。
function humanSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

export default function FileFullPreview({ instances }: { instances: InstanceView[] }) {
  const { t } = useTranslation()
  const msg = useMessage()
  // 选中的在线 bukkit 源 serverId
  const [serverId, setServerId] = useState('')
  // 已建扫描任务 id（驱动清单轮询）；为 null 表示尚未发起全量预览
  const [taskId, setTaskId] = useState<number | null>(null)
  // 展开看合并的追踪文件 path
  const [expanded, setExpanded] = useState<string | null>(null)
  // 已对当前 taskId 触发过 cancel，避免清单到位后重复取消
  const cancelledRef = useRef<number | null>(null)

  // 仅在线 bukkit 可作扫描源（bungee 无 plugins 配置树可抓）
  const onlineSources = useMemo(
    () => instances.filter((i) => i.status === 'online' && i.role === 'bukkit'),
    [instances],
  )

  // 源缺省取首个在线源
  useEffect(() => {
    if (!serverId && onlineSources.length > 0) setServerId(onlineSources[0].serverId)
  }, [onlineSources, serverId])

  // 源实例（取 namespace / group）：实例列表跨 namespace，须随选中源带上。
  const source = useMemo(
    () => onlineSources.find((i) => i.serverId === serverId),
    [onlineSources, serverId],
  )

  // 建扫描任务（两段式第一段：命令 agent 扫描其 plugins/ 回传全量清单）。
  const scanMut = useMutation({
    mutationFn: () => {
      const ns = source?.namespace ?? ''
      const group = source?.group ?? ''
      return createScanTask(serverId, ns, { scope: 'server', group, target: serverId })
    },
    onSuccess: (task) => {
      cancelledRef.current = null
      setExpanded(null)
      setTaskId(task.id)
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  // 轮询任务详情至清单到位 / 失败（2s）。
  const taskQuery = useQuery({
    queryKey: ['file-full-scan', taskId],
    queryFn: () => getReverseFetchTask(taskId!),
    enabled: taskId != null,
    refetchInterval: (q) => {
      const s = q.state.data?.status
      if (s && SCAN_DONE.has(s)) return false
      return 2000
    },
  })
  const task = taskQuery.data

  // 有效树（FR-45）：取追踪文件 path 集（仅在已选源后拉）。
  const effectiveQuery = useQuery({
    queryKey: ['file-full-effective', source?.namespace, serverId],
    queryFn: () => effectiveFiles({ namespace: source?.namespace ?? '', serverId }),
    enabled: !!serverId && !!source,
  })

  // 追踪 path 集 + 追踪文件合并视图映射（点开时取对应合并结果）。
  const trackedPaths = useMemo(
    () => new Set((effectiveQuery.data?.files ?? []).map((f) => f.path)),
    [effectiveQuery.data],
  )
  const trackedItems = useMemo(() => {
    const map = new Map<string, EffectiveFileItem>()
    for (const f of effectiveQuery.data?.files ?? []) map.set(f.path, f)
    return map
  }, [effectiveQuery.data])

  // 清单到位（pending-review）即取消任务：预览只读、不 ingest，避免遗留 pending 任务。
  // 失败/已取消等终态无需再取消（避免对终态任务发无效 cancel）。
  useEffect(() => {
    if (!task || task.status !== 'pending-review') return
    if (cancelledRef.current === task.id) return
    cancelledRef.current = task.id
    cancelReverseFetchTask(task.id).catch((e: Error) => msg.showError(e.message))
    // msg 为稳定引用（useMessage 每次新建但仅 toast，无需入依赖）；按 task.id 去重即可
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [task?.id, task?.status])

  // 全量清单（清单到位才有）：按 path 排序，逐文件标 tracked。
  const rows = useMemo(() => {
    const files: ReverseFetchScanFileView[] =
      task?.status === 'pending-review' ? (task.files ?? []) : []
    return [...files]
      .sort((a, b) => a.path.localeCompare(b.path))
      .map((f) => ({ ...f, tracked: trackedPaths.has(f.path) }))
  }, [task, trackedPaths])

  const scanning = task != null && !SCAN_DONE.has(task.status)
  const failed = task?.status === 'failed'

  function onTrigger() {
    if (!serverId || !source) {
      msg.showError(t('filePreview.fullSourceRequired'))
      return
    }
    scanMut.mutate()
  }

  return (
    <div className="flex-1 flex flex-col min-h-0">
      {/* 源选择 + 触发 */}
      <div className="flex-shrink-0 flex flex-wrap items-center gap-2 px-3 py-1.5 border-b border-border bg-muted/20">
        <span className="text-xs text-muted-foreground">{t('filePreview.fullSourceLabel')}</span>
        <select
          aria-label={t('filePreview.fullSourceLabel')}
          className="h-7 rounded border border-input bg-background px-2 text-xs"
          value={serverId}
          onChange={(e) => {
            setServerId(e.target.value)
            setTaskId(null)
            setExpanded(null)
          }}
        >
          <option value="">{t('filePreview.selectPlaceholder')}</option>
          {onlineSources.map((i) => (
            <option key={i.serverId} value={i.serverId}>
              {i.serverId}（{i.group}）
            </option>
          ))}
        </select>
        <Button
          size="sm"
          onClick={onTrigger}
          disabled={!serverId || scanMut.isPending || scanning}
        >
          {scanMut.isPending || scanning
            ? t('filePreview.fullScanning')
            : t('filePreview.fullTriggerBtn')}
        </Button>
        {task?.status === 'pending-review' && (
          <Badge variant="outline" className="text-xs">
            {t('filePreview.fullCount', { count: rows.length })}
          </Badge>
        )}
      </div>

      {/* 全量清单 */}
      {onlineSources.length === 0 ? (
        <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
          {t('filePreview.fullNoOnline')}
        </div>
      ) : taskId == null && !scanMut.isPending ? (
        <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
          {t('filePreview.fullEmpty')}
        </div>
      ) : scanning || scanMut.isPending ? (
        <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
          {t('filePreview.fullScanningHint', { serverId })}
        </div>
      ) : failed ? (
        <div className="flex-1 flex items-center justify-center text-sm text-destructive">
          {t('filePreview.fullFailed')}
        </div>
      ) : rows.length === 0 ? (
        <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
          {t('filePreview.fullNoFiles')}
        </div>
      ) : (
        <ScrollArea className="flex-1">
          <ul className="p-3 space-y-1.5">
            {rows.map((f) => (
              <li key={f.path} className="rounded border border-border">
                <div className="flex items-center justify-between gap-2 px-2 py-1.5 text-xs">
                  <span className="font-mono break-all flex-1 min-w-0">{f.path}</span>
                  <span className="flex items-center gap-1.5 shrink-0">
                    <span className="text-muted-foreground font-mono">{humanSize(f.size)}</span>
                    {f.tracked ? (
                      <Badge variant="secondary" className="text-[0.6rem]">
                        {t('filePreview.trackedBadge')}
                      </Badge>
                    ) : (
                      <Badge variant="outline" className="text-[0.6rem] text-amber-600 border-amber-300">
                        {t('filePreview.untrackedBadge')}
                      </Badge>
                    )}
                    {f.tracked ? (
                      <Button
                        size="xs"
                        variant="ghost"
                        onClick={() => setExpanded(expanded === f.path ? null : f.path)}
                      >
                        {expanded === f.path
                          ? t('filePreview.fullCollapse')
                          : t('filePreview.fullViewMerge')}
                      </Button>
                    ) : (
                      <Link
                        to="/reverse-fetch"
                        className="text-[0.65rem] text-blue-600 underline-offset-2 hover:underline"
                      >
                        {t('filePreview.fullToReverseFetch')}
                      </Link>
                    )}
                  </span>
                </div>
                {/* 追踪文件点开：复用 FR-45 合并卡片（合并结果 + 逐键来源） */}
                {f.tracked && expanded === f.path && trackedItems.get(f.path) && (
                  <div className="border-t border-border p-2">
                    <FileMergeCard file={trackedItems.get(f.path)!} />
                  </div>
                )}
              </li>
            ))}
          </ul>
        </ScrollArea>
      )}
    </div>
  )
}
