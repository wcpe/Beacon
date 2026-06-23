// 上传预览审批模态（FR-66）：选完文件后不直接传，先全量列待传清单
// （path + 大小 + 文本/二进制 Badge + 超大 Badge）+ 选中文件内容只读预览
// （前端 FileReader 读，文本截断防卡、二进制/超大不读内容只标记）；
// 底部「我已审阅」checkbox + 「确认导入」（审阅后启用）→ 经 onConfirm 才真正调 importFiles。

import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import type { ImportFileEntry } from '../../api/client'
import { useMessage } from '../../components/useMessage'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

// 文本预览截断上限（字节）：超过只读前 N 字节并标记截断，防大文件卡渲染。
const TEXT_PREVIEW_LIMIT = 2000
// 超大判定阈值（字节）：超过则视为超大，不读内容只标记（避免 FileReader 读巨文件占内存）。
const OVERSIZE_LIMIT = 256 * 1024

// 人类可读文件大小（字节 → B/KB/MB）。
function humanSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

// 按 MIME 与后缀粗判是否文本文件：text/* 与常见文本配置后缀算文本，余者按二进制处理（不读内容）。
const TEXT_EXTS = [
  '.yml', '.yaml', '.json', '.properties', '.txt', '.conf', '.cfg',
  '.ini', '.toml', '.xml', '.md', '.csv', '.env', '.sql', '.log',
]
function isTextFile(file: File): boolean {
  if (file.type.startsWith('text/')) return true
  if (file.type === 'application/json') return true
  const name = file.name.toLowerCase()
  return TEXT_EXTS.some((ext) => name.endsWith(ext))
}

// 单个待传文件的预览态：基础元数据 + 异步读出的文本内容（二进制/超大为空）。
interface PreviewItem {
  path: string
  size: number
  isText: boolean
  oversize: boolean
  // 已读出的文本内容（截断后）；null 表示尚未读或不读。
  content: string | null
  // 内容是否被截断。
  truncated: boolean
}

// 由 entries 推导初始预览元数据（不含内容，内容随选中异步读取）。
function toPreviewItems(entries: ImportFileEntry[]): PreviewItem[] {
  return entries.map((e) => {
    const oversize = e.file.size > OVERSIZE_LIMIT
    const text = isTextFile(e.file)
    return {
      path: e.path,
      size: e.file.size,
      isText: text,
      oversize,
      content: null,
      truncated: false,
    }
  })
}

export default function ImportPreviewModal({
  open,
  entries,
  namespace,
  group,
  pending,
  onConfirm,
  onCancel,
}: {
  // 模态开合
  open: boolean
  // 待传文件清单（来自上传 input）
  entries: ImportFileEntry[]
  // 目标环境 / 组（只展示）
  namespace: string
  group: string
  // 确认导入进行中（禁用按钮）
  pending: boolean
  // 审阅确认 → 上层据此调 importFiles
  onConfirm: () => void
  // 取消（不入库）
  onCancel: () => void
}) {
  const { t } = useTranslation()
  const msg = useMessage()
  // 预览清单元数据
  const items = useMemo(() => toPreviewItems(entries), [entries])
  // 当前选中查看内容的文件 path
  const [selectedPath, setSelectedPath] = useState<string>('')
  // 已读出的文本内容缓存：path → { content, truncated }
  const [contents, setContents] = useState<Record<string, { content: string; truncated: boolean }>>({})
  // 当前选中文件正在读取
  const [reading, setReading] = useState(false)
  // 审阅闸：勾选才放行确认
  const [reviewed, setReviewed] = useState(false)

  // 打开 / 清单变化时复位选中、内容缓存与审阅闸。
  useEffect(() => {
    if (open) {
      setSelectedPath(items[0]?.path ?? '')
      setContents({})
      setReviewed(false)
    }
  }, [open, items])

  const selectedItem = items.find((i) => i.path === selectedPath)

  // 选中文本文件且未读过 → 异步 FileReader 读内容（文本截断、二进制/超大跳过）。
  useEffect(() => {
    if (!selectedItem || !selectedPath) return
    if (!selectedItem.isText || selectedItem.oversize) return
    if (contents[selectedPath]) return
    const entry = entries.find((e) => e.path === selectedPath)
    if (!entry) return
    let cancelled = false
    setReading(true)
    // 只读前 TEXT_PREVIEW_LIMIT 字节，按字节切片防大文件卡。
    const blob = entry.file.slice(0, TEXT_PREVIEW_LIMIT)
    const reader = new FileReader()
    reader.onload = () => {
      if (cancelled) return
      const text = typeof reader.result === 'string' ? reader.result : ''
      const truncated = entry.file.size > TEXT_PREVIEW_LIMIT
      setContents((prev) => ({ ...prev, [selectedPath]: { content: text, truncated } }))
      setReading(false)
    }
    reader.onerror = () => {
      if (cancelled) return
      setReading(false)
      msg.showError(t('configs.importPreviewReading'))
    }
    reader.readAsText(blob)
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedPath, selectedItem?.isText, selectedItem?.oversize])

  // 总大小（字节）。
  const totalSize = useMemo(() => items.reduce((sum, i) => sum + i.size, 0), [items])

  const selectedContent = selectedPath ? contents[selectedPath] : undefined

  function handleConfirm() {
    if (!reviewed) {
      msg.showError(t('configs.importPreviewReviewRequired'))
      return
    }
    onConfirm()
  }

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) onCancel() }}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{t('configs.importPreviewTitle')}</DialogTitle>
        </DialogHeader>

        {/* 概要：目标 + 总数 + 总大小 */}
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <span className="font-mono">{namespace} / {group || '—'}</span>
          <span>·</span>
          <span>{t('configs.importPreviewSummary', { count: items.length, size: humanSize(totalSize) })}</span>
        </div>

        <div className="grid grid-cols-2 gap-3 min-h-0">
          {/* 左：待传清单全量列 */}
          <div className="flex flex-col min-h-0">
            <div className="px-1 pb-1 text-xs font-medium text-muted-foreground">
              {t('configs.importPreviewManifest')}
            </div>
            <ScrollArea className="h-72 rounded border border-border">
              <ul className="divide-y divide-border">
                {items.map((it) => (
                  <li key={it.path}>
                    <button
                      type="button"
                      onClick={() => setSelectedPath(it.path)}
                      aria-label={it.path}
                      className={
                        'flex w-full items-center gap-2 px-2 py-1.5 text-left text-sm hover:bg-muted/40' +
                        (it.path === selectedPath ? ' bg-muted/60' : '')
                      }
                    >
                      <span className="flex-1 min-w-0 truncate font-mono text-xs" title={it.path}>
                        {it.path}
                      </span>
                      {it.isText ? (
                        <Badge variant="secondary" className="text-[0.65rem]">
                          {t('configs.importPreviewBadgeText')}
                        </Badge>
                      ) : (
                        <Badge variant="outline" className="text-[0.65rem]">
                          {t('configs.importPreviewBadgeBinary')}
                        </Badge>
                      )}
                      {it.oversize && (
                        <Badge variant="destructive" className="text-[0.65rem]">
                          {t('configs.importPreviewBadgeOversize')}
                        </Badge>
                      )}
                      <span className="w-16 shrink-0 text-right font-mono text-[0.7rem] text-muted-foreground">
                        {humanSize(it.size)}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            </ScrollArea>
          </div>

          {/* 右：选中文件内容只读预览 */}
          <div className="flex flex-col min-h-0">
            <div className="px-1 pb-1 text-xs font-medium text-muted-foreground">
              {t('configs.importPreviewContent')}
            </div>
            <div className="h-72 rounded border border-border overflow-hidden">
              {!selectedItem ? (
                <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
                  {t('configs.importPreviewSelectHint')}
                </div>
              ) : !selectedItem.isText ? (
                <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
                  {t('configs.importPreviewBinaryHint')}
                </div>
              ) : selectedItem.oversize ? (
                <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
                  {t('configs.importPreviewOversizeHint', { size: humanSize(OVERSIZE_LIMIT) })}
                </div>
              ) : reading && !selectedContent ? (
                <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
                  {t('configs.importPreviewReading')}
                </div>
              ) : (
                <div className="flex h-full flex-col">
                  {selectedContent?.truncated && (
                    <div className="shrink-0 px-2 py-1 text-[0.7rem] text-amber-600 bg-amber-50">
                      {t('configs.importPreviewTruncated', { size: humanSize(TEXT_PREVIEW_LIMIT) })}
                    </div>
                  )}
                  <ScrollArea className="flex-1 min-h-0">
                    <pre className="px-2 py-1 font-mono text-xs whitespace-pre-wrap break-all">
                      {selectedContent?.content ?? ''}
                    </pre>
                  </ScrollArea>
                </div>
              )}
            </div>
          </div>
        </div>

        <DialogFooter className="items-center sm:justify-between">
          {/* 审阅闸：勾选才放行确认导入 */}
          <label className="flex items-center gap-2 text-xs text-muted-foreground select-none">
            <input
              type="checkbox"
              checked={reviewed}
              onChange={(e) => setReviewed(e.target.checked)}
            />
            {t('configs.importPreviewReviewed')}
          </label>
          <div className="flex gap-2">
            <Button type="button" variant="outline" onClick={onCancel} disabled={pending}>
              {t('configs.importPreviewCancel')}
            </Button>
            <Button type="button" onClick={handleConfirm} disabled={!reviewed || pending}>
              {pending ? t('configs.importing') : t('configs.importPreviewConfirm')}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
