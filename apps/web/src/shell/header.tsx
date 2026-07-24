// 双段页眉（FR-187/188/189 + FR-193～196）：
// 段 1 = 全局运维指标真数据；段 2 = 汉堡(小屏) + 环境过滤 + 搜索/语言/通知/刷新 + 用户/演示控件。
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Menu, Search, X } from 'lucide-react'

import { Badge, Button } from '@beacon/ui'

import { isDemoMode } from '../demo-mode'
import { useShellStore } from '../store'
import CommandPalette from './command-palette'
import EnvFilter from './env-filter'
import LanguageMenu from './language-menu'
import MetricsStrip from './metrics-strip'
import NotificationsMenu from './notifications-menu'
import OperatorMenu from './operator-menu'
import PageRefreshButton from './page-refresh-button'
import ScenarioSwitcher from './scenario-switcher'

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
      <LanguageMenu />
      <NotificationsMenu />
      <PageRefreshButton />
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
      <MetricsStrip />
      {/* FR-191：段 2 与内容区同底、无横线、无毛玻璃半透明底 */}
      <header className="flex h-12 items-center gap-3 bg-transparent px-[22px]">
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
        <EnvFilter />
        <div className="flex flex-1 items-center justify-end gap-1.5">
          <HeaderUtilities
            onOpenSearch={() => {
              setPaletteOpen(true)
            }}
          />
          {demo ? (
            <>
              <Badge variant="secondary">{t('common.demoMode')}</Badge>
              <ScenarioSwitcher />
            </>
          ) : (
            <OperatorMenu />
          )}
        </div>
      </header>
      <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
    </div>
  )
}
