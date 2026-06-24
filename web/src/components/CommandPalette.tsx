// 全局命令面板（FR-83）：Ctrl/Cmd+K 唤起的浮层，单输入框即时聚合搜索 +
// 纯键盘可达（上下选 / 回车跳转 / Esc 关）。聚合导航、配置·文件、服务器、审计动作四类，
// 选中回车跳到对应页（带深链查询参数）。纯前端，复用既有列表端点、不加后端。
//
// 数据源：面板打开时一次性拉 listConfigs/listFiles/listInstances（无 namespace = 全部），
// 客户端按 query 子串过滤；拉取失败降级为只剩导航 + 审计动作，面板仍可用、不被阻断。

import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Dialog as DialogPrimitive } from 'radix-ui'
import { SearchIcon } from 'lucide-react'
import { listConfigs, listFiles, listInstances } from '@/api/client'
import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'
import {
  buildItems,
  filterItems,
  groupItems,
  type AuditActionSource,
  type CommandGroup,
  type NavSource,
} from '@/lib/commandPalette'

// 导航目标（路径 + i18n key）——与 Layout 侧栏同源的可跳转页集合。
const NAV_TARGETS: Array<{ to: string; labelKey: string }> = [
  { to: '/dashboard', labelKey: 'nav.dashboard' },
  { to: '/configs', labelKey: 'nav.configs' },
  { to: '/file-preview', labelKey: 'nav.filePreview' },
  { to: '/imprint', labelKey: 'nav.imprint' },
  { to: '/reverse-fetch', labelKey: 'nav.reverseFetchTask' },
  { to: '/servers', labelKey: 'nav.servers' },
  { to: '/topology', labelKey: 'nav.topology' },
  { to: '/zones', labelKey: 'nav.zones' },
  { to: '/audits', labelKey: 'nav.audits' },
  { to: '/service-analysis', labelKey: 'nav.serviceAnalysis' },
  { to: '/api-keys', labelKey: 'nav.apiKeys' },
  { to: '/namespaces', labelKey: 'nav.namespaces' },
  { to: '/settings', labelKey: 'nav.settings' },
  { to: '/system', labelKey: 'nav.systemObservability' },
]

// 审计动作快捷项：高频运维动作，跳审计页并带 action 过滤；动作名复用既有 audit.action.* 映射。
const AUDIT_ACTION_KEYS = [
  'config.publish',
  'config.rollback',
  'instance.offline',
  'zone.assign',
  'apikey.create',
] as const

// 分组标题 i18n key
const GROUP_TITLE_KEY: Record<CommandGroup, string> = {
  navigation: 'commandPalette.groupNavigation',
  config: 'commandPalette.groupConfig',
  server: 'commandPalette.groupServer',
  audit: 'commandPalette.groupAudit',
}

export interface CommandPaletteProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export default function CommandPalette({ open, onOpenChange }: CommandPaletteProps) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [query, setQuery] = useState('')
  const [activeIndex, setActiveIndex] = useState(0)
  const listRef = useRef<HTMLDivElement>(null)

  // 打开时拉一次配置 / 文件 / 实例（无 namespace = 全部）；失败不抛、降级为空，面板仍可用。
  const configs = useQuery({
    queryKey: ['command-palette', 'configs'],
    queryFn: () => listConfigs({}),
    enabled: open,
    staleTime: 30_000,
  })
  const files = useQuery({
    queryKey: ['command-palette', 'files'],
    queryFn: () => listFiles({}),
    enabled: open,
    staleTime: 30_000,
  })
  const instances = useQuery({
    queryKey: ['command-palette', 'instances'],
    queryFn: () => listInstances({}),
    enabled: open,
    staleTime: 30_000,
  })

  // 静态源：导航 + 审计动作（译名一并算入，供过滤匹配）
  const navItems: NavSource[] = useMemo(
    () => NAV_TARGETS.map((n) => ({ to: n.to, label: t(n.labelKey) })),
    [t],
  )
  const auditActions: AuditActionSource[] = useMemo(
    () => AUDIT_ACTION_KEYS.map((a) => ({ action: a, label: t(`audit.action.${a}`) })),
    [t],
  )

  // 归一 + 过滤 + 分组：数据变化或输入变化时重算
  const flat = useMemo(
    () =>
      buildItems({
        navItems,
        auditActions,
        configs: configs.data,
        files: files.data,
        instances: instances.data,
      }),
    [navItems, auditActions, configs.data, files.data, instances.data],
  )
  const filtered = useMemo(() => filterItems(flat, query), [flat, query])
  const grouped = useMemo(() => groupItems(filtered), [filtered])

  // 过滤结果变化时把选中下标夹在合法区间内（避免越界）
  useEffect(() => {
    setActiveIndex((idx) => (filtered.length === 0 ? 0 : Math.min(idx, filtered.length - 1)))
  }, [filtered.length])

  // 每次打开重置输入与选中
  useEffect(() => {
    if (open) {
      setQuery('')
      setActiveIndex(0)
    }
  }, [open])

  // 执行选中项：跳转并关闭面板
  function runItem(to: string) {
    onOpenChange(false)
    navigate(to)
  }

  // 键盘导航：上下移动选中、回车执行、Esc 由 Dialog 自身处理（这里仅拦 Enter/方向键）
  function onKeyDown(e: React.KeyboardEvent) {
    if (filtered.length === 0) return
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setActiveIndex((idx) => (idx + 1) % filtered.length)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setActiveIndex((idx) => (idx - 1 + filtered.length) % filtered.length)
    } else if (e.key === 'Enter') {
      e.preventDefault()
      const item = filtered[activeIndex]
      if (item) runItem(item.to)
    }
  }

  // 选中项滚动进视野
  useEffect(() => {
    const el = listRef.current?.querySelector<HTMLElement>('[data-active="true"]')
    // jsdom 不实现 scrollIntoView，做存在性守卫
    el?.scrollIntoView?.({ block: 'nearest' })
  }, [activeIndex])

  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/20 data-open:animate-in data-open:fade-in-0" />
        <DialogPrimitive.Content
          aria-label={t('commandPalette.title')}
          onKeyDown={onKeyDown}
          className="fixed top-[15%] left-1/2 z-50 flex max-h-[70vh] w-full max-w-xl -translate-x-1/2 flex-col overflow-hidden rounded-xl bg-popover text-sm text-popover-foreground ring-1 ring-foreground/10 outline-none data-open:animate-in data-open:fade-in-0 data-open:zoom-in-95"
        >
          {/* 无障碍标题（视觉隐藏）：Radix Dialog 要求有 Title */}
          <DialogPrimitive.Title className="sr-only">
            {t('commandPalette.title')}
          </DialogPrimitive.Title>

          {/* 搜索输入区 */}
          <div className="flex items-center gap-2 border-b px-3 py-2">
            <SearchIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
            <Input
              autoFocus
              value={query}
              onChange={(e) => {
                setQuery(e.target.value)
                setActiveIndex(0)
              }}
              placeholder={t('commandPalette.placeholder')}
              aria-label={t('commandPalette.placeholder')}
              className="h-7 border-0 px-0 ring-0 focus-visible:border-0 focus-visible:ring-0"
            />
          </div>

          {/* 结果列表 */}
          <div ref={listRef} className="min-h-0 flex-1 overflow-y-auto p-1.5">
            {filtered.length === 0 ? (
              <div className="px-3 py-8 text-center text-muted-foreground">
                {t('commandPalette.empty')}
              </div>
            ) : (
              grouped.map((g) => (
                <div key={g.group} className="mb-1">
                  <div className="px-2 py-1 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
                    {t(GROUP_TITLE_KEY[g.group])}
                  </div>
                  {g.items.map((item) => {
                    // 扁平下标 = 该项在过滤后列表中的位置（分组保持过滤序）
                    const flatIndex = filtered.indexOf(item)
                    const active = flatIndex === activeIndex
                    return (
                      <button
                        key={item.id}
                        type="button"
                        data-active={active}
                        role="option"
                        aria-selected={active}
                        onMouseEnter={() => setActiveIndex(flatIndex)}
                        onClick={() => runItem(item.to)}
                        className={cn(
                          'flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left',
                          active ? 'bg-accent text-accent-foreground' : 'hover:bg-accent/60',
                        )}
                      >
                        <span className="truncate">{item.title}</span>
                        {item.subtitle && (
                          <span className="ml-auto truncate text-xs text-muted-foreground">
                            {item.subtitle}
                          </span>
                        )}
                      </button>
                    )
                  })}
                </div>
              ))
            )}
          </div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  )
}
