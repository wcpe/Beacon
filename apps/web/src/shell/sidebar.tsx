// 侧栏导航：品牌区 + 顶层运维总览 + 四大域分组（集群 / 可观测 / 交付 / 系统）。
// 整栏折叠沿用 Zustand 的 sidebarCollapsed，由页眉按钮切换。
import { useTranslation } from 'react-i18next'
import { NavLink } from 'react-router-dom'

import { DASHBOARD_PAGE, NAV_GROUPS, type NavPage } from '../routes'
import { useShellStore } from '../store'

function SidebarNavItem({ page }: { page: NavPage }) {
  const { t } = useTranslation()
  return (
    <NavLink
      end
      to={page.path}
      className={({ isActive }) =>
        [
          'flex items-center gap-2 rounded-md px-2 py-2 text-sm transition-colors',
          isActive ? 'bg-accent text-accent-foreground' : 'text-muted-foreground hover:bg-muted',
        ].join(' ')
      }
    >
      <page.icon className="size-4" />
      <span>{t(page.titleKey)}</span>
    </NavLink>
  )
}

export default function Sidebar() {
  const { t } = useTranslation()
  const sidebarCollapsed = useShellStore((state) => state.sidebarCollapsed)
  return (
    <aside
      className={[
        'fixed inset-y-0 left-0 hidden w-56 overflow-y-auto border-r bg-card px-3 py-4',
        sidebarCollapsed ? 'md:hidden' : 'md:block',
      ].join(' ')}
    >
      <div className="mb-5 flex items-center gap-2 px-2">
        <div className="flex size-8 items-center justify-center rounded-md bg-primary text-primary-foreground">
          B
        </div>
        <div>
          <div className="text-sm font-semibold">Beacon</div>
          <div className="text-xs text-muted-foreground">{t('common.consoleName')}</div>
        </div>
      </div>
      <nav className="grid gap-1">
        <SidebarNavItem page={DASHBOARD_PAGE} />
        {NAV_GROUPS.map((group) => (
          <div key={group.titleKey} className="mt-3">
            <div className="px-2 pb-1 text-xs font-medium text-muted-foreground">
              {t(group.titleKey)}
            </div>
            <div className="grid gap-1">
              {group.pages.map((page) => (
                <SidebarNavItem key={page.path} page={page} />
              ))}
            </div>
          </div>
        ))}
      </nav>
    </aside>
  )
}
