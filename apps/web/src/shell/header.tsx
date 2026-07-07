// 页眉：导航折叠开关 + 控制台标识
import { PanelLeftClose, PanelLeftOpen } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@beacon/ui'

import { useShellStore } from '../store'

export default function Header() {
  const { t } = useTranslation()
  const sidebarCollapsed = useShellStore((state) => state.sidebarCollapsed)
  const toggleSidebar = useShellStore((state) => state.toggleSidebar)
  const toggleLabel = sidebarCollapsed ? t('common.sidebar.expand') : t('common.sidebar.collapse')
  return (
    <header className="flex min-h-12 items-center justify-between border-b bg-background px-4">
      <div className="flex min-w-0 items-center gap-2">
        <Button
          aria-label={toggleLabel}
          size="icon"
          title={toggleLabel}
          variant="ghost"
          onClick={toggleSidebar}
        >
          {sidebarCollapsed ? (
            <PanelLeftOpen className="size-4" />
          ) : (
            <PanelLeftClose className="size-4" />
          )}
        </Button>
        <span className="truncate text-sm text-muted-foreground">{t('common.consoleName')}</span>
      </div>
    </header>
  )
}
