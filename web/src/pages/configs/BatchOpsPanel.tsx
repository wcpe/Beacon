/**
 * 配置批量操作面板（FR-74）
 *
 * 自带多选 checkbox 列表 + 批量操作栏（删除 / 禁用 / 启用 / 导出）：
 *   - 空选时全部按钮禁用；
 *   - 批量删除前弹轻量确认（本 FR 自带，不依赖 FR-76）；
 *   - 导出 = 前端逐项拉选中内容打包成 JSON 后 Blob 下载（best-effort，无新依赖）。
 *
 * 删除 / 禁用 / 启用经 batchConfigs 一事务原子完成（后端 POST /admin/v1/configs/batch）。
 * 本组件只管「列表选择 + 批量栏」，不碰编辑器的单条删除逻辑（减少与 FR-76 的 rebase 冲突）。
 */

import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQueryClient } from '@tanstack/react-query'

import { batchConfigs, getConfig, type BatchAction } from '../../api/client'
import type { ConfigView } from '../../api/types'
import { useMessage } from '../../components/useMessage'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'

export default function BatchOpsPanel({ configs }: { configs: ConfigView[] }) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const msg = useMessage()

  // 选中集合：以 config id 为键
  const [selected, setSelected] = useState<Set<number>>(new Set())
  // 删除确认对话框开合
  const [confirmDeleteOpen, setConfirmDeleteOpen] = useState(false)
  // 导出进行中（导出为前端多次拉取，给一个忙态防重复点）
  const [exporting, setExporting] = useState(false)

  const selectedIds = useMemo(() => Array.from(selected), [selected])
  const hasSelection = selectedIds.length > 0
  const allSelected = configs.length > 0 && selectedIds.length === configs.length

  // 切换单项选中
  const toggleOne = useCallback((id: number) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }, [])

  // 全选 / 取消全选
  const toggleAll = useCallback(() => {
    setSelected((prev) => (prev.size === configs.length ? new Set() : new Set(configs.map((c) => c.id))))
  }, [configs])

  // 批量删除 / 禁用 / 启用 mutation（一事务原子，后端 batch 端点）
  const batchMut = useMutation({
    mutationFn: ({ action, ids }: { action: BatchAction; ids: number[] }) => batchConfigs(action, ids),
    onSuccess: (r) => {
      const key =
        r.action === 'delete'
          ? 'configs.batchMsgDeleted'
          : r.action === 'disable'
            ? 'configs.batchMsgDisabled'
            : 'configs.batchMsgEnabled'
      msg.showSuccess(t(key, { count: r.count }))
      setSelected(new Set())
      setConfirmDeleteOpen(false)
      qc.invalidateQueries({ queryKey: ['configs'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  // 触发删除：先弹轻量确认（FR-74 自带，不依赖 FR-76）
  const requestDelete = useCallback(() => {
    if (hasSelection) setConfirmDeleteOpen(true)
  }, [hasSelection])

  // 导出：逐项拉内容打包成 JSON，Blob 下载（best-effort，无新依赖）
  const exportSelected = useCallback(async () => {
    if (!hasSelection || exporting) return
    setExporting(true)
    try {
      const items = await Promise.all(selectedIds.map((id) => getConfig(id)))
      const blob = new Blob([JSON.stringify(items, null, 2)], { type: 'application/json' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `configs-export-${Date.now()}.json`
      a.click()
      URL.revokeObjectURL(url)
      msg.showSuccess(t('configs.batchMsgExported', { count: items.length }))
    } catch (e) {
      msg.showError((e as Error).message)
    } finally {
      setExporting(false)
    }
  }, [hasSelection, exporting, selectedIds, msg, t])

  return (
    <div className="flex flex-col rounded-lg border border-border bg-card overflow-hidden">
      {/* 标题 + 提示 */}
      <div className="px-3 py-2 border-b border-border bg-muted/30">
        <div className="text-sm font-medium">{t('configs.batchTitle')}</div>
        <div className="text-xs text-muted-foreground mt-0.5">{t('configs.batchHint')}</div>
      </div>

      {/* 批量操作栏 */}
      <div className="flex items-center gap-2 px-3 py-2 border-b border-border">
        <span className="text-xs text-muted-foreground mr-auto">
          {t('configs.batchSelected', { count: selectedIds.length })}
        </span>
        <Button
          size="sm"
          variant="destructive"
          disabled={!hasSelection || batchMut.isPending}
          onClick={requestDelete}
        >
          {t('configs.batchDelete')}
        </Button>
        <Button
          size="sm"
          variant="outline"
          disabled={!hasSelection || batchMut.isPending}
          onClick={() => batchMut.mutate({ action: 'disable', ids: selectedIds })}
        >
          {t('configs.batchDisable')}
        </Button>
        <Button
          size="sm"
          variant="outline"
          disabled={!hasSelection || batchMut.isPending}
          onClick={() => batchMut.mutate({ action: 'enable', ids: selectedIds })}
        >
          {t('configs.batchEnable')}
        </Button>
        <Button size="sm" variant="outline" disabled={!hasSelection || exporting} onClick={exportSelected}>
          {t('configs.batchExport')}
        </Button>
      </div>

      {/* 多选列表 */}
      {configs.length === 0 ? (
        <div className="px-3 py-6 text-center text-xs text-muted-foreground">
          {t('configs.batchEmpty')}
        </div>
      ) : (
        <div className="max-h-64 overflow-y-auto">
          {/* 表头：全选 */}
          <div className="flex items-center gap-2 px-3 py-1.5 border-b border-border text-xs font-medium text-muted-foreground sticky top-0 bg-card">
            <Checkbox
              aria-label={t('configs.batchSelectAll')}
              checked={allSelected}
              onCheckedChange={toggleAll}
            />
            <span className="flex-1">dataId</span>
            <span className="w-16 text-right">{t('configs.batchColScope')}</span>
            <span className="w-12 text-right">{t('configs.batchColEnabled')}</span>
          </div>
          {/* 行 */}
          {configs.map((c) => (
            <label
              key={c.id}
              className="flex items-center gap-2 px-3 py-1.5 text-sm hover:bg-muted/40 cursor-pointer"
            >
              <Checkbox
                aria-label={c.dataId}
                checked={selected.has(c.id)}
                onCheckedChange={() => toggleOne(c.id)}
              />
              <span className="flex-1 truncate">{c.dataId}</span>
              <span className="w-16 text-right text-xs text-muted-foreground">{c.scopeLevel}</span>
              <span className="w-12 text-right text-xs text-muted-foreground">
                {c.enabled ? t('configs.batchEnabledYes') : t('configs.batchEnabledNo')}
              </span>
            </label>
          ))}
        </div>
      )}

      {/* 批量删除轻量确认（FR-74 自带，不依赖 FR-76） */}
      <AlertDialog open={confirmDeleteOpen} onOpenChange={setConfirmDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('configs.batchConfirmDeleteTitle')}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('configs.batchConfirmDeleteDesc', { count: selectedIds.length })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('configs.batchConfirmCancel')}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={(e) => {
                // 阻止 AlertDialog 默认关闭，待请求成功后再关，避免误以为已删
                e.preventDefault()
                batchMut.mutate({ action: 'delete', ids: selectedIds })
              }}
            >
              {batchMut.isPending ? t('configs.batchProcessing') : t('configs.batchConfirmOk')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
