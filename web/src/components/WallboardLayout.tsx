// NOC 大屏布局（FR-92）：无侧栏、极简页眉（退出大屏 + 主题切换），承载只读大屏看板。
// 大屏面向值班墙呈现，去掉一切导航与操作入口；主题切换保留以便暗色机房使用。

import { Link, Outlet } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Moon, Sun, LogOut } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { setTheme, usePreferences } from '@/state/preferences'

export default function WallboardLayout() {
  const { t } = useTranslation()
  const { theme } = usePreferences()

  return (
    <div className="flex h-screen flex-col overflow-hidden bg-background text-foreground">
      <header className="flex shrink-0 items-center gap-3 border-b px-6 py-3">
        <span className="text-base font-semibold">{t('wallboard.title')}</span>
        <div className="ml-auto flex items-center gap-1">
          {/* 主题切换：暗色机房常用，故大屏也保留 */}
          <Button
            variant="ghost"
            size="icon"
            aria-label={theme === 'dark' ? t('preferences.themeToLight') : t('preferences.themeToDark')}
            title={theme === 'dark' ? t('preferences.themeToLight') : t('preferences.themeToDark')}
            onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')}
          >
            {theme === 'dark' ? <Sun className="size-4" /> : <Moon className="size-4" />}
          </Button>
          {/* 退出大屏：返回常规管理台 */}
          <Button asChild variant="outline" size="sm">
            <Link to="/dashboard">
              <LogOut className="size-4" />
              {t('wallboard.exit')}
            </Link>
          </Button>
        </div>
      </header>
      <main className="min-w-0 flex-1 overflow-y-auto p-8">
        <Outlet />
      </main>
    </div>
  )
}
