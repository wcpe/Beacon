// 页眉界面偏好控件（FR-92）：暗色主题切换 + 表格密度切换 + 大屏入口。
// 偏好读写走 state/preferences 单一真源；本组件只负责呈现与触发，不持有状态。

import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Moon, Sun, Rows3, Rows4, Monitor } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { setDensity, setTheme, usePreferences } from '@/state/preferences'

export default function HeaderControls() {
  const { t } = useTranslation()
  const { theme, density } = usePreferences()

  // 主题切换：浅 ↔ 暗反转
  function toggleTheme() {
    setTheme(theme === 'dark' ? 'light' : 'dark')
  }

  // 密度切换：舒适 ↔ 紧凑反转
  function toggleDensity() {
    setDensity(density === 'compact' ? 'comfortable' : 'compact')
  }

  return (
    <div className="ml-auto flex items-center gap-1">
      {/* 主题切换：暗色时显示太阳（点击回浅色），浅色时显示月亮（点击进暗色） */}
      <Button
        variant="ghost"
        size="icon"
        aria-label={theme === 'dark' ? t('preferences.themeToLight') : t('preferences.themeToDark')}
        title={theme === 'dark' ? t('preferences.themeToLight') : t('preferences.themeToDark')}
        onClick={toggleTheme}
      >
        {theme === 'dark' ? <Sun className="size-4" /> : <Moon className="size-4" />}
      </Button>
      {/* 密度切换：紧凑时显示舒适图标（点击回舒适），舒适时显示紧凑图标（点击进紧凑） */}
      <Button
        variant="ghost"
        size="icon"
        aria-label={
          density === 'compact'
            ? t('preferences.densityToComfortable')
            : t('preferences.densityToCompact')
        }
        title={
          density === 'compact'
            ? t('preferences.densityToComfortable')
            : t('preferences.densityToCompact')
        }
        onClick={toggleDensity}
      >
        {density === 'compact' ? <Rows3 className="size-4" /> : <Rows4 className="size-4" />}
      </Button>
      {/* 大屏入口（FR-92）：进入 NOC 只读看板 */}
      <Button asChild variant="ghost" size="icon" title={t('preferences.wallboardEnter')}>
        <Link to="/wallboard" aria-label={t('preferences.wallboardEnter')}>
          <Monitor className="size-4" />
        </Link>
      </Button>
    </div>
  )
}
