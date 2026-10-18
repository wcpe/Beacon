// 单层页眉（FR-187/188/189 + FR-193～196）：
// 左侧 = 控制面状态点（内含 CPU/内存弹层）+ 环境/命名空间选择器；右侧 = 搜索/语言/通知/刷新 + 账户入口。
// 不放面包屑：页面身份由各内容页的 PageHeader 大标题承担（见 docs/UX.md §4）。
// 页眉保持透明、不铺底色也不加分隔线：内容滚动容器起点正好在页眉下沿（不会从页眉背后穿过），
// 加底色或 backdrop-blur 没有实际收益，且会与 y=8 起浮的侧栏卡片形成错位。
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Menu, Search, X } from 'lucide-react'

import { Button } from '@beacon/ui'

import { isDemoMode } from '../demo-mode'
import { useShellStore } from '../store'
import CommandPalette from './command-palette'
import EnvFilter from './env-filter'
import LanguageMenu from './language-menu'
import MetricsStrip from './metrics-strip'
import ControlPlaneResources from './control-plane-resources'
import NotificationsMenu from './notifications-menu'
import OperatorMenu from './operator-menu'
import PageRefreshButton from './page-refresh-button'

/** 段 2 右侧工具：搜索 / 语言 / 通知 / 刷新（FR-193～196） */
function HeaderUtilities({ onOpenSearch }: { onOpenSearch: () => void }) {
  const { t } = useTranslation()
  return (
    <>
      <Button
        type="button"
        size="icon-sm"
        variant="ghost"
        aria-label={t('common.header.search')}
        title={`${t('common.header.search')} (Ctrl+K)`}
        data-slot="header-search"
        onClick={onOpenSearch}
      >
        <Search className="size-4" />
      </Button>
      {/* 语言是硬功能入口，不能在小屏直接消失：sm 起就保留（手机横屏/平板可用）。
          刷新则只在 lg 起显示，小屏可下拉整页刷新，避免占用宝贵横向空间。 */}
      <div data-slot="header-language-compact" className="hidden sm:inline-flex">
        <LanguageMenu />
      </div>
      <NotificationsMenu />
      <div data-slot="header-refresh-compact" className="hidden lg:inline-flex">
        <PageRefreshButton />
      </div>
    </>
  )
}

export default function Header() {
  const { t } = useTranslation()
  const demo = isDemoMode()
  const mobileNavOpen = useShellStore((state) => state.mobileNavOpen)
  const toggleMobileNav = useShellStore((state) => state.toggleMobileNav)
  const [paletteOpen, setPaletteOpen] = useState(false)

  // 全局 Ctrl/Cmd+K 唤起命令面板（FR-193）
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && (e.key === 'k' || e.key === 'K')) {
        e.preventDefault()
        setPaletteOpen(true)
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => {
      window.removeEventListener('keydown', onKeyDown)
    }
  }, [])

  return (
    <div data-slot="app-header" className="shrink-0">
      {/* 单层页眉：控制面状态点（内含运维指标弹层）+ 环境/命名空间 + 右侧工具。
          原「指标条 + 工具行」两层已合并，省下 36px 垂直空间。 */}
      <header className="flex h-12 items-center gap-0.5 bg-transparent px-3 sm:gap-1.5 sm:px-4 md:gap-1 lg:gap-1 lg:px-4 xl:gap-2 xl:px-[22px]">
        {/* 小屏汉堡：打开/关闭抽屉侧栏（FR-189） */}
        <Button
          type="button"
          size="icon-sm"
          variant="ghost"
          className="md:hidden"
          aria-label={mobileNavOpen ? t('common.sidebar.closeMobile') : t('common.sidebar.openMobile')}
          aria-expanded={mobileNavOpen}
          onClick={toggleMobileNav}
        >
          {mobileNavOpen ? <X className="size-4" /> : <Menu className="size-4" />}
        </Button>
        {/* 控制面状态点：也是运维指标的入口（点开看 Agent 在线 / 待确认 / 告警 / 变更单） */}
        <MetricsStrip />
        <span aria-hidden className="h-4 w-px shrink-0 bg-border" />
        {/* 环境 / 命名空间选择器固定在左侧：所有页面（含 /approvals）统一渲染 */}
        <EnvFilter />
        <ControlPlaneResources />
        <div className="flex min-w-0 flex-1 items-center justify-end gap-1">
          <HeaderUtilities
            onOpenSearch={() => {
              setPaletteOpen(true)
            }}
          />
          {!demo && <OperatorMenu />}
        </div>
      </header>
      <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
    </div>
  )
}
