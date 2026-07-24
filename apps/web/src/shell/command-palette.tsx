// 页眉命令面板（FR-193）：Dialog + 输入过滤 + 键盘导航；导航为主，可选 server/审计深链。
import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router-dom'
import { CornerDownLeft, Search } from 'lucide-react'

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  Input,
  cn,
} from '@beacon/ui'

import {
  buildPaletteItems,
  groupItems,
  type CommandGroup,
  type CommandItem,
} from '../features/command-palette/items'

interface CommandPaletteProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

function groupLabelKey(group: CommandGroup): string {
  if (group === 'nav') {
    return 'common.commandPalette.groupNav'
  }
  if (group === 'servers') {
    return 'common.commandPalette.groupServers'
  }
  return 'common.commandPalette.groupAudits'
}

export default function CommandPalette({ open, onOpenChange }: CommandPaletteProps) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [query, setQuery] = useState('')
  const [activeIndex, setActiveIndex] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLDivElement>(null)

  const resolveTitle = (item: CommandItem): string => {
    if (item.titleKey !== undefined) {
      return t(item.titleKey)
    }
    if (item.group === 'audits') {
      return t(`observability.audits.action.${item.title ?? ''}`, {
        defaultValue: item.title ?? item.id,
      })
    }
    if (item.group === 'servers') {
      return t('common.commandPalette.searchServers', { q: item.title ?? '' })
    }
    return item.title ?? item.id
  }

  const flatItems = useMemo(
    () => buildPaletteItems(query, resolveTitle),
    // resolveTitle 依赖 t；query / 语言变化时重建
    [query, t],
  )
  const groups = useMemo(() => groupItems(flatItems), [flatItems])

  // 打开时复位；关闭后清空查询
  useEffect(() => {
    if (open) {
      setQuery('')
      setActiveIndex(0)
      // 下一帧聚焦输入
      const id = window.setTimeout(() => {
        inputRef.current?.focus()
      }, 0)
      return () => {
        window.clearTimeout(id)
      }
    }
    return undefined
  }, [open])

  // 结果变化时钳制 activeIndex
  useEffect(() => {
    setActiveIndex((prev) => {
      if (flatItems.length === 0) {
        return 0
      }
      return Math.min(prev, flatItems.length - 1)
    })
  }, [flatItems.length])

  // 活动项滚入视野
  useEffect(() => {
    const root = listRef.current
    if (!root) {
      return
    }
    const el = root.querySelector(`[data-palette-index="${String(activeIndex)}"]`)
    if (el instanceof HTMLElement) {
      el.scrollIntoView({ block: 'nearest' })
    }
  }, [activeIndex])

  const runItem = (item: CommandItem) => {
    onOpenChange(false)
    navigate(item.to)
  }

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      if (flatItems.length === 0) {
        return
      }
      setActiveIndex((i) => (i + 1) % flatItems.length)
      return
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      if (flatItems.length === 0) {
        return
      }
      setActiveIndex((i) => (i - 1 + flatItems.length) % flatItems.length)
      return
    }
    if (e.key === 'Enter') {
      e.preventDefault()
      if (flatItems.length === 0) {
        return
      }
      runItem(flatItems[activeIndex])
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        showCloseButton={false}
        className="top-[18%] max-w-lg translate-y-0 gap-0 overflow-hidden p-0 sm:max-w-lg"
        data-slot="command-palette"
        onKeyDown={onKeyDown}
      >
        <DialogHeader className="sr-only">
          <DialogTitle>{t('common.commandPalette.title')}</DialogTitle>
          <DialogDescription>{t('common.commandPalette.hint')}</DialogDescription>
        </DialogHeader>
        <div className="flex items-center gap-2 border-b border-border px-3 py-2.5">
          <Search className="size-4 shrink-0 text-ink-4" aria-hidden />
          <Input
            ref={inputRef}
            value={query}
            onChange={(e) => {
              setQuery(e.target.value)
              setActiveIndex(0)
            }}
            placeholder={t('common.commandPalette.placeholder')}
            aria-label={t('common.commandPalette.placeholder')}
            className="h-9 border-0 bg-transparent px-0 shadow-none focus-visible:ring-0"
            data-slot="command-palette-input"
          />
          <kbd className="hidden shrink-0 rounded border border-border bg-muted px-1.5 py-0.5 text-[10px] text-ink-4 sm:inline">
            Esc
          </kbd>
        </div>
        <div
          ref={listRef}
          className="max-h-[min(22rem,50vh)] overflow-y-auto p-1.5"
          role="listbox"
          aria-label={t('common.commandPalette.title')}
        >
          {flatItems.length === 0 ? (
            <p className="px-3 py-6 text-center text-sm text-ink-4">{t('common.commandPalette.empty')}</p>
          ) : (
            groups.map((g) => (
              <div key={g.group} className="mb-1.5">
                <div className="px-2.5 py-1 text-[11px] font-medium text-ink-4">{t(groupLabelKey(g.group))}</div>
                {g.items.map((item) => {
                  const index = flatItems.indexOf(item)
                  const active = index === activeIndex
                  return (
                    <button
                      key={item.id}
                      type="button"
                      role="option"
                      aria-selected={active}
                      data-palette-index={index}
                      className={cn(
                        'flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-sm transition-colors',
                        active ? 'bg-brand-50 text-ink-1' : 'text-ink-2 hover:bg-surface-2',
                      )}
                      onMouseEnter={() => {
                        setActiveIndex(index)
                      }}
                      onClick={() => {
                        runItem(item)
                      }}
                    >
                      <span className="min-w-0 flex-1 truncate font-medium">{resolveTitle(item)}</span>
                      <span className="max-w-[40%] truncate text-[11px] text-ink-4">
                        {item.group === 'nav' && item.subtitle !== undefined
                          ? t(item.subtitle)
                          : item.to}
                      </span>
                      {active ? <CornerDownLeft className="size-3.5 shrink-0 text-ink-4" aria-hidden /> : null}
                    </button>
                  )
                })}
              </div>
            ))
          )}
        </div>
        <div className="flex items-center gap-3 border-t border-border px-3 py-1.5 text-[11px] text-ink-4">
          <span>{t('common.commandPalette.hintNav')}</span>
          <span className="ml-auto">{t('common.commandPalette.hint')}</span>
        </div>
      </DialogContent>
    </Dialog>
  )
}
