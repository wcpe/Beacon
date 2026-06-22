// 编辑器标签栏：展示已打开的配置标签，支持点击切换、关闭与右键菜单（关闭当前/其他/全部）。

import { useTranslation } from 'react-i18next'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import type { OpenTab } from './types'

export type TabContextAction = 'close' | 'closeOthers' | 'closeAll'

export default function ConfigTabBar({
  openTabs,
  activeTabKey,
  contextTabId,
  showTabMenu,
  onSelect,
  onClose,
  onContextMenu,
  onContextAction,
}: {
  openTabs: OpenTab[]
  activeTabKey: string | null
  contextTabId: number | null
  showTabMenu: boolean
  onSelect: (configId: number) => void
  onClose: (configId: number) => void
  onContextMenu: (configId: number) => void
  onContextAction: (action: TabContextAction) => void
}) {
  const { t } = useTranslation()
  return (
    <div className="flex-shrink-0 flex rounded-lg border border-border overflow-hidden bg-card h-9">
      {openTabs.length === 0 ? (
        <div className="px-4 py-2 text-sm text-muted-foreground">{t('configs.tabEmpty')}</div>
      ) : (
        openTabs.map((tab) => {
          const isActive = String(tab.configId) === activeTabKey
          return (
            <DropdownMenu key={tab.configId}>
              <DropdownMenuTrigger asChild>
                <button
                  type="button"
                  className={cn(
                    'flex items-center gap-1.5 px-3 text-sm whitespace-nowrap border-b-2 transition-colors select-none h-full',
                    isActive
                      ? 'border-b-primary bg-muted/50 font-medium text-foreground'
                      : 'border-b-transparent text-muted-foreground hover:text-foreground hover:bg-muted/30',
                  )}
                  onClick={() => onSelect(tab.configId)}
                  onContextMenu={(e) => {
                    e.preventDefault()
                    onContextMenu(tab.configId)
                  }}
                >
                  <span className="max-w-[100px] truncate">{tab.dataId}</span>
                  <Badge variant="outline" className="text-[0.6rem] h-4 px-1 shrink-0">
                    {tab.scopeLevel}
                  </Badge>
                  <span
                    className="ml-0.5 text-xs text-muted-foreground/60 hover:text-destructive cursor-pointer shrink-0"
                    onClick={(e) => {
                      e.stopPropagation()
                      onClose(tab.configId)
                    }}
                  >
                    ✕
                  </span>
                </button>
              </DropdownMenuTrigger>
              {contextTabId === tab.configId && showTabMenu && (
                <DropdownMenuContent align="start" className="w-40" sideOffset={4}>
                  <DropdownMenuItem onClick={() => onContextAction('close')}>{t('configs.tabCloseCurrent')}</DropdownMenuItem>
                  <DropdownMenuItem onClick={() => onContextAction('closeOthers')}>
                    {t('configs.tabCloseOthers')}
                  </DropdownMenuItem>
                  <DropdownMenuItem onClick={() => onContextAction('closeAll')}>{t('configs.tabCloseAll')}</DropdownMenuItem>
                </DropdownMenuContent>
              )}
            </DropdownMenu>
          )
        })
      )}
    </div>
  )
}
