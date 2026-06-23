// 反向抓取冲突审核台（FR-60，任务 conflict-review 时）：列冲突 path；逐文件 Monaco DiffEditor
// （左抓取值 ⟷ 右目标已有版本）；逐文件「我已审阅」自审门 → overwrite（取新值，自审带 fetchedMd5）/ keep（保留已有）；
// 全部冲突文件有决定才能 resolveConflicts 落库。复用 ImprintDiffPanel 的 DiffEditor + 自审门范式。

import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { conflictDiff, listConflicts, resolveConflicts } from '../../api/client'
import type { ResolveDecision } from '../../api/types'
import { useMessage } from '../../components/useMessage'
import CodeEditor from '../../components/CodeEditor'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ScrollArea } from '@/components/ui/scroll-area'

// 单文件处置态：未决（undefined）/ overwrite / keep；overwrite 须先审阅。
type Action = 'overwrite' | 'keep' | undefined

// 按文件后缀推断 Monaco 语言（yaml/json，余者纯文本）。
function langOf(path: string): string {
  if (path.endsWith('.json')) return 'json'
  if (path.endsWith('.yml') || path.endsWith('.yaml')) return 'yaml'
  return 'plaintext'
}

export default function ReverseFetchConflictPanel({
  taskId,
  onResolved,
}: {
  // conflict-review 任务 id
  taskId: number
  // 落库成功回调（任务转 done/ingesting，上层据轮询续走）
  onResolved: () => void
}) {
  const { t } = useTranslation()
  const msg = useMessage()
  const qc = useQueryClient()
  // 当前查看 diff 的冲突文件
  const [activePath, setActivePath] = useState<string | null>(null)
  // 逐文件处置：path → action
  const [actions, setActions] = useState<Record<string, Action>>({})
  // 逐文件审阅闸：path → 是否已勾「我已审阅」（取新值前置）
  const [reviewed, setReviewed] = useState<Record<string, boolean>>({})
  // 逐文件 fetchedMd5 缓存：随各文件 diff 加载逐步累积，供 overwrite 自审带 md5。
  // overwrite 须先审阅（须先看过该文件 diff），故选 overwrite 时该 md5 必已就绪。
  const [md5Map, setMd5Map] = useState<Record<string, string>>({})

  // 冲突 path 清单。
  const conflictsQuery = useQuery({
    queryKey: ['reverse-fetch-conflicts', taskId],
    queryFn: () => listConflicts(taskId),
  })
  const conflicts = useMemo(() => conflictsQuery.data ?? [], [conflictsQuery.data])

  // 缺省选首个冲突文件。
  const currentPath = activePath ?? conflicts[0] ?? null

  // 当前冲突文件 diff（抓取值 ⟷ 目标已有版本）。
  const diffQuery = useQuery({
    queryKey: ['reverse-fetch-conflict-diff', taskId, currentPath],
    queryFn: () => conflictDiff(taskId, currentPath!),
    enabled: !!currentPath,
  })
  const diff = diffQuery.data

  // diff 加载成功即缓存该文件 fetchedMd5（累积，供后续 resolve 取各 overwrite 文件自审 md5）。
  useEffect(() => {
    if (diff && currentPath) {
      setMd5Map((prev) =>
        prev[currentPath] === diff.fetchedMd5 ? prev : { ...prev, [currentPath]: diff.fetchedMd5 },
      )
    }
  }, [diff, currentPath])

  function setAction(path: string, action: Action) {
    setActions((prev) => ({ ...prev, [path]: action }))
  }

  // 全部冲突文件均有决定才放行 resolve。
  const allDecided = conflicts.length > 0 && conflicts.every((p) => actions[p])

  const resolveMut = useMutation({
    mutationFn: () => {
      const decisions: ResolveDecision[] = conflicts.map((path) => {
        const action = actions[path]
        // overwrite 须带自审 md5（= 该文件 diff 的 fetchedMd5）；keep 无须 md5。
        if (action === 'overwrite') {
          return { path, action, reviewedMd5: md5Map[path] ?? '' }
        }
        return { path, action: 'keep' }
      })
      return resolveConflicts(taskId, decisions)
    },
    onSuccess: (res) => {
      msg.showSuccess(t('reverseFetchTask.msgResolved', { created: res.created, updated: res.updated }))
      qc.invalidateQueries({ queryKey: ['reverse-fetch-tasks'] })
      qc.invalidateQueries({ queryKey: ['reverse-fetch-task', taskId] })
      onResolved()
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onResolve() {
    if (!allDecided) {
      msg.showError(t('reverseFetchTask.errResolveIncomplete'))
      return
    }
    resolveMut.mutate()
  }

  if (conflictsQuery.isLoading) {
    return (
      <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">
        {t('common.loading')}
      </div>
    )
  }

  return (
    <div className="flex flex-1 flex-col min-h-0">
      <div className="flex flex-wrap items-center gap-3 px-3 py-2 border-b border-border bg-muted/20">
        <div className="text-sm font-medium">{t('reverseFetchTask.conflictTitle')}</div>
        <span className="text-xs text-muted-foreground">{t('reverseFetchTask.conflictHint')}</span>
        <Button
          size="sm"
          className="ml-auto"
          onClick={onResolve}
          disabled={resolveMut.isPending || !allDecided}
        >
          {resolveMut.isPending
            ? t('reverseFetchTask.resolving')
            : t('reverseFetchTask.resolveBtn')}
        </Button>
      </div>

      <div className="flex flex-1 min-h-0">
        {/* 左：冲突文件清单 + 逐文件处置 */}
        <div className="w-72 shrink-0 border-r border-border flex flex-col min-h-0">
          <div className="px-3 py-1.5 text-xs font-medium text-muted-foreground border-b border-border">
            {t('reverseFetchTask.conflictListTitle')}（{conflicts.length}）
          </div>
          <ScrollArea className="flex-1 min-h-0">
            <ul className="divide-y divide-border">
              {conflicts.map((path) => {
                const action = actions[path]
                return (
                  <li
                    key={path}
                    className={
                      'px-3 py-2 cursor-pointer hover:bg-muted/30' +
                      (path === currentPath ? ' bg-muted/50' : '')
                    }
                    onClick={() => setActivePath(path)}
                  >
                    <div className="font-mono text-xs break-all">{path}</div>
                    <div className="mt-1 flex items-center gap-1">
                      {action === 'overwrite' && (
                        <Badge variant="destructive" className="text-[0.65rem]">
                          {t('reverseFetchTask.actionOverwrite')}
                        </Badge>
                      )}
                      {action === 'keep' && (
                        <Badge variant="secondary" className="text-[0.65rem]">
                          {t('reverseFetchTask.actionKeep')}
                        </Badge>
                      )}
                      {!action && (
                        <span className="text-[0.65rem] text-muted-foreground">
                          {t('reverseFetchTask.decideRequired')}
                        </span>
                      )}
                    </div>
                  </li>
                )
              })}
            </ul>
          </ScrollArea>
        </div>

        {/* 右：当前文件 diff + 审阅自审门 + 处置选择 */}
        <div className="flex flex-1 flex-col min-h-0">
          {!currentPath ? (
            <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">
              {t('reverseFetchTask.conflictSelectHint')}
            </div>
          ) : (
            <>
              <div className="flex flex-wrap items-center gap-3 px-3 py-1.5 border-b border-border text-xs">
                <span className="font-mono break-all">{currentPath}</span>
                {diff && (
                  <span className="text-muted-foreground">
                    {t('reverseFetchTask.diffMeta', {
                      fetched: diff.fetchedMd5.slice(0, 8),
                      existing: diff.existingMd5.slice(0, 8),
                      version: diff.version,
                    })}
                  </span>
                )}
                <label className="ml-auto flex items-center gap-1 text-xs text-muted-foreground select-none">
                  <input
                    type="checkbox"
                    checked={!!reviewed[currentPath]}
                    disabled={!diff || diffQuery.isFetching}
                    onChange={(e) =>
                      setReviewed((prev) => ({ ...prev, [currentPath]: e.target.checked }))
                    }
                  />
                  {t('reverseFetchTask.reviewedCheckbox')}
                </label>
                <Button
                  size="xs"
                  variant={actions[currentPath] === 'overwrite' ? 'destructive' : 'outline'}
                  disabled={!reviewed[currentPath]}
                  onClick={() => {
                    if (!reviewed[currentPath]) {
                      msg.showError(t('reverseFetchTask.errReviewFirst'))
                      return
                    }
                    setAction(currentPath, 'overwrite')
                  }}
                >
                  {t('reverseFetchTask.actionOverwrite')}
                </Button>
                <Button
                  size="xs"
                  variant={actions[currentPath] === 'keep' ? 'secondary' : 'outline'}
                  onClick={() => setAction(currentPath, 'keep')}
                >
                  {t('reverseFetchTask.actionKeep')}
                </Button>
              </div>
              <div className="flex-1 min-h-0">
                {diffQuery.isLoading ? (
                  <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
                    {t('reverseFetchTask.diffParsing')}
                  </div>
                ) : diffQuery.isError ? (
                  <div className="flex h-full items-center justify-center text-sm text-destructive">
                    {(diffQuery.error as Error).message}
                  </div>
                ) : diff ? (
                  <CodeEditor
                    original={diff.fetchedContent}
                    modified={diff.existingContent}
                    language={langOf(currentPath)}
                  />
                ) : null}
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  )
}
