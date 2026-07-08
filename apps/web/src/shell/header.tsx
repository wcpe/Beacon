// 页眉（对齐 B 版）：56px、半透明白 + backdrop-blur；左侧导航折叠开关 + 控制台名；
// 右侧控制面在线药丸 + 常驻演示模式徽标（FR-159）+ 四态场景切换器。
import { PanelLeftClose, PanelLeftOpen } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Badge, Button } from '@beacon/ui'

import { useShellStore } from '../store'
import ScenarioSwitcher from './scenario-switcher'

export default function Header() {
  const { t } = useTranslation()
  const sidebarCollapsed = useShellStore((state) => state.sidebarCollapsed)
  const toggleSidebar = useShellStore((state) => state.toggleSidebar)
  const toggleLabel = sidebarCollapsed ? t('common.sidebar.expand') : t('common.sidebar.collapse')
  return (
    <header className="sticky top-0 z-10 flex h-14 items-center gap-3.5 border-b border-border bg-background/85 px-[22px] backdrop-blur-md">
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
        <span className="truncate text-sm text-ink-3">{t('common.consoleName')}</span>
      </div>
      <div className="flex flex-1 items-center justify-end gap-2.5">
        {/* 控制面在线状态药丸（绿底 + 呼吸圆点） */}
        <Badge variant="ok" className="gap-1.5">
          <span className="size-[7px] rounded-full bg-current shadow-[0_0_0_3px_color-mix(in_srgb,currentColor_18%,transparent)]" />
          {t('common.controlPlaneOnline')}
        </Badge>
        <Badge variant="secondary">{t('common.demoMode')}</Badge>
        <ScenarioSwitcher />
      </div>
    </header>
  )
}
