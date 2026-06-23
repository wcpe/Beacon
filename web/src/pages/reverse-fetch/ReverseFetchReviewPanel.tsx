// 反向抓取审核台（FR-60，任务 pending-review 时）：全量列扫描清单每文件
// （path/size/isText/超阈值红标/ignoredByRule），逐行复选 + 逐项/目录（前缀）忽略
// + 「保存为持久规则」（exact/prefix）+ 超阈值勾确认 → submitReverseFetchTask 提交选定集。
// 默认勾选策略：文本文件且未超阈值且未命中持久规则的默认选中；超阈值 / 命中规则 / 二进制默认排除（可见）。

import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQueryClient } from '@tanstack/react-query'

import { createIgnoreRule, submitReverseFetchTask } from '../../api/client'
import type {
  IgnoreRuleType,
  ReverseFetchScanFileView,
  ReverseFetchScope,
  ReverseFetchTaskView,
} from '../../api/types'
import { useMessage } from '../../components/useMessage'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { ScrollArea } from '@/components/ui/scroll-area'

// 取文件所在目录前缀（含末尾 /）；顶层文件返回空串（无目录可前缀忽略）。
function dirPrefix(path: string): string {
  const i = path.lastIndexOf('/')
  return i < 0 ? '' : path.slice(0, i + 1)
}

// 人类可读文件大小（清单大小为字节）。
function humanSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

// 文件默认是否选中：文本 + 未超阈值 + 未命中持久规则才默认选中。
function defaultChecked(f: ReverseFetchScanFileView): boolean {
  return f.isText && !f.overThreshold && !f.ignoredByRule
}

export default function ReverseFetchReviewPanel({
  task,
  onSubmitted,
}: {
  // pending-review 任务（含全量清单 files）
  task: ReverseFetchTaskView
  // 提交成功回调（任务转 fetching，上层据轮询续走）
  onSubmitted: () => void
}) {
  const { t } = useTranslation()
  const msg = useMessage()
  const qc = useQueryClient()
  // 逐行选定态：path → 是否选中
  const [selected, setSelected] = useState<Record<string, boolean>>({})
  // 超阈值纳入确认闸：选定集含超阈值文件时须先勾
  const [confirmOver, setConfirmOver] = useState(false)

  const files = task.files

  // 清单到达 / 切换任务时初始化默认选定（默认勾选策略）。
  useEffect(() => {
    const init: Record<string, boolean> = {}
    for (const f of files) init[f.path] = defaultChecked(f)
    setSelected(init)
    setConfirmOver(false)
  }, [task.id, files])

  function toggle(path: string, checked: boolean) {
    setSelected((prev) => ({ ...prev, [path]: checked }))
  }

  // 忽略此项（临时不选）。
  function ignoreItem(path: string) {
    setSelected((prev) => ({ ...prev, [path]: false }))
  }

  // 忽略此目录（前缀）：取消选中该前缀下全部文件。
  function ignoreDir(path: string) {
    const prefix = dirPrefix(path)
    if (!prefix) {
      ignoreItem(path)
      return
    }
    setSelected((prev) => {
      const next = { ...prev }
      for (const f of files) {
        if (f.path.startsWith(prefix)) next[f.path] = false
      }
      return next
    })
  }

  const selectAll = () => {
    const next: Record<string, boolean> = {}
    for (const f of files) next[f.path] = true
    setSelected(next)
  }
  const clearAll = () => {
    const next: Record<string, boolean> = {}
    for (const f of files) next[f.path] = false
    setSelected(next)
  }

  // 选定 path 集合（保序）。
  const selectedPaths = useMemo(
    () => files.filter((f) => selected[f.path]).map((f) => f.path),
    [files, selected],
  )
  // 选定集是否含超阈值文件（含则提交须带 confirmOverThreshold）。
  const selectedOverThreshold = useMemo(
    () => files.filter((f) => selected[f.path] && f.overThreshold).length,
    [files, selected],
  )

  // 保存持久规则（exact 单文件 / prefix 目录）。
  const ruleMut = useMutation({
    mutationFn: ({ ruleType, pattern }: { ruleType: IgnoreRuleType; pattern: string }) =>
      createIgnoreRule({
        namespace: task.namespace,
        scope: task.scope as ReverseFetchScope,
        group: task.group,
        target: task.scope === 'server' ? task.target : undefined,
        ruleType,
        pattern,
      }),
    onSuccess: () => {
      msg.showSuccess(t('reverseFetchTask.msgRuleSaved'))
      qc.invalidateQueries({ queryKey: ['reverse-fetch-ignore-rules'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  const submitMut = useMutation({
    mutationFn: () =>
      submitReverseFetchTask(task.id, {
        selectedPaths,
        confirmOverThreshold: selectedOverThreshold > 0,
      }),
    onSuccess: () => {
      msg.showSuccess(t('reverseFetchTask.msgSubmitted'))
      qc.invalidateQueries({ queryKey: ['reverse-fetch-tasks'] })
      onSubmitted()
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onSubmit() {
    if (selectedPaths.length === 0) {
      msg.showError(t('reverseFetchTask.errNoSelection'))
      return
    }
    // 选定集含超阈值文件但未勾确认 → 前端拦（后端亦 400）。
    if (selectedOverThreshold > 0 && !confirmOver) {
      msg.showError(t('reverseFetchTask.errConfirmOverThreshold'))
      return
    }
    submitMut.mutate()
  }

  return (
    <div className="flex flex-1 flex-col min-h-0 gap-2">
      {/* 顶部操作条 */}
      <div className="flex flex-wrap items-center gap-3 px-3 py-2 border-b border-border bg-muted/20">
        <div className="text-sm font-medium">{t('reverseFetchTask.reviewTitle')}</div>
        <Button size="xs" variant="outline" onClick={selectAll}>
          {t('reverseFetchTask.selectAll')}
        </Button>
        <Button size="xs" variant="outline" onClick={clearAll}>
          {t('reverseFetchTask.clearAll')}
        </Button>
        <span className="text-xs text-muted-foreground">
          {t('reverseFetchTask.selectedSummary', { count: selectedPaths.length })}
        </span>
        {selectedOverThreshold > 0 && (
          <label className="flex items-center gap-1.5 text-xs text-destructive select-none">
            <input
              type="checkbox"
              checked={confirmOver}
              onChange={(e) => setConfirmOver(e.target.checked)}
            />
            {t('reverseFetchTask.confirmOverThreshold')}（
            {t('reverseFetchTask.overThresholdSelected', { count: selectedOverThreshold })}）
          </label>
        )}
        <Button
          size="sm"
          className="ml-auto"
          onClick={onSubmit}
          disabled={submitMut.isPending || selectedPaths.length === 0}
        >
          {submitMut.isPending
            ? t('reverseFetchTask.submitting')
            : t('reverseFetchTask.submitBtn')}
        </Button>
      </div>

      <p className="px-3 text-xs text-muted-foreground">{t('reverseFetchTask.reviewHint')}</p>

      {/* 清单全量列（大清单用 ScrollArea + 简洁行） */}
      {files.length === 0 ? (
        <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">
          {t('reverseFetchTask.emptyManifest')}
        </div>
      ) : (
        <ScrollArea className="flex-1 min-h-0 border-t border-border">
          <ul className="divide-y divide-border">
            {files.map((f) => {
              const checked = !!selected[f.path]
              return (
                <li
                  key={f.path}
                  className="flex items-center gap-2 px-3 py-1.5 text-sm hover:bg-muted/30"
                >
                  <Checkbox
                    checked={checked}
                    onCheckedChange={(v) => toggle(f.path, v === true)}
                    aria-label={f.path}
                  />
                  <span
                    className={
                      'flex-1 min-w-0 truncate font-mono text-xs' +
                      (f.ignoredByRule ? ' text-muted-foreground line-through' : '')
                    }
                    title={f.path}
                  >
                    {f.path}
                  </span>
                  {f.overThreshold && (
                    <Badge variant="destructive" className="text-[0.65rem]">
                      {t('reverseFetchTask.badgeOverThreshold')}
                    </Badge>
                  )}
                  {f.ignoredByRule && (
                    <Badge variant="secondary" className="text-[0.65rem]">
                      {t('reverseFetchTask.badgeIgnored')}
                    </Badge>
                  )}
                  {!f.isText && (
                    <Badge variant="outline" className="text-[0.65rem]">
                      {t('reverseFetchTask.badgeBinary')}
                    </Badge>
                  )}
                  <span className="w-16 shrink-0 text-right font-mono text-[0.7rem] text-muted-foreground">
                    {humanSize(f.size)}
                  </span>
                  <div className="flex shrink-0 gap-1">
                    <Button
                      size="icon-xs"
                      variant="ghost"
                      title={t('reverseFetchTask.ignoreItemBtn')}
                      aria-label={`${t('reverseFetchTask.ignoreItemBtn')} ${f.path}`}
                      onClick={() => ignoreItem(f.path)}
                    >
                      ×
                    </Button>
                    <Button
                      size="xs"
                      variant="ghost"
                      onClick={() => ignoreDir(f.path)}
                      title={t('reverseFetchTask.ignoreDirBtn')}
                    >
                      {t('reverseFetchTask.ignoreDirBtn')}
                    </Button>
                    <Button
                      size="xs"
                      variant="ghost"
                      disabled={ruleMut.isPending}
                      onClick={() => ruleMut.mutate({ ruleType: 'exact', pattern: f.path })}
                      title={t('reverseFetchTask.saveRuleExactBtn')}
                    >
                      {t('reverseFetchTask.saveRuleBtn')}
                    </Button>
                    {dirPrefix(f.path) && (
                      <Button
                        size="xs"
                        variant="ghost"
                        disabled={ruleMut.isPending}
                        onClick={() =>
                          ruleMut.mutate({ ruleType: 'prefix', pattern: dirPrefix(f.path) })
                        }
                        title={t('reverseFetchTask.saveRulePrefixBtn')}
                      >
                        {t('reverseFetchTask.saveRulePrefixBtn')}
                      </Button>
                    )}
                  </div>
                </li>
              )
            })}
          </ul>
        </ScrollArea>
      )}
    </div>
  )
}
