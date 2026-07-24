// 侧栏导航（FR-186 / FR-189）：
// 桌面：展开约 232px / 折叠约 60px 图标轨，宽度与主区同步动画。
// 小屏：默认隐藏；打开时为全宽文案抽屉（不走图标轨）。
// 品牌仅 logo + Beacon；无内部分割线；底栏左版本号、右开源许可。
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link, NavLink } from 'react-router-dom'

import { fetchSystemStatus } from '../api/system'
import { DASHBOARD_PAGE, NAV_GROUPS, type NavPage } from '../routes'
import { useShellStore } from '../store'

/** 展开宽 / 折叠图标轨宽（与 app-shell 主区 pl 一致） */
export const SIDEBAR_WIDTH_EXPANDED = 232
export const SIDEBAR_WIDTH_COLLAPSED = 60

/** 开源许可（站内页 FR-190；仓库根 LICENSE 为 MIT） */
const LICENSE_LABEL = 'MIT'
const LICENSE_PATH = '/license'

function SidebarNavItem({
  page,
  collapsed,
  onNavigate,
}: {
  page: NavPage
  collapsed: boolean
  onNavigate?: () => void
}) {
  const { t } = useTranslation()
  const label = t(page.titleKey)
  return (
    <NavLink
      end
      to={page.path}
      title={label}
      aria-label={label}
      onClick={() => {
        onNavigate?.()
      }}
      className={({ isActive }) =>
        [
          'group flex items-center rounded-lg text-[12.5px] transition-colors',
          collapsed ? 'justify-center px-0 py-2' : 'gap-2.5 px-2.5 py-[7px]',
          isActive
            ? 'bg-sidebar-accent font-semibold text-sidebar-accent-foreground'
            : 'text-ink-2 hover:bg-surface-2 hover:text-ink-1',
        ].join(' ')
      }
    >
      {({ isActive }) => (
        <>
          <page.icon
            className={['size-4 shrink-0', isActive ? 'text-brand' : 'text-ink-4'].join(' ')}
          />
          {!collapsed && (
            <>
              <span className="truncate">{label}</span>
              {page.Badge && <page.Badge />}
            </>
          )}
        </>
      )}
    </NavLink>
  )
}

/** 底栏：左版本号、右开源许可；折叠时仅显示版本短标 */
function SidebarFooter({ collapsed }: { collapsed: boolean }) {
  const { t } = useTranslation()
  const statusQuery = useQuery({
    queryKey: ['shell', 'sidebar', 'version'],
    queryFn: fetchSystemStatus,
    staleTime: 60_000,
    refetchInterval: 120_000,
    retry: 1,
  })
  const version = statusQuery.data?.version
  const versionLabel = version === undefined || version === '' ? '—' : version.startsWith('v') ? version : `v${version}`

  if (collapsed) {
    return (
      <div
        data-slot="sidebar-footer"
        className="flex shrink-0 flex-col items-center gap-1 px-1 py-2.5"
        title={`${versionLabel} · ${LICENSE_LABEL}`}
      >
        <span className="max-w-full truncate text-[10px] font-medium tabular-nums text-ink-4">
          {versionLabel}
        </span>
        <Link
          to={LICENSE_PATH}
          className="text-[10px] text-ink-4 underline-offset-2 hover:text-ink-2 hover:underline"
          aria-label={t('common.sidebar.license')}
        >
          {LICENSE_LABEL}
        </Link>
      </div>
    )
  }

  return (
    <div
      data-slot="sidebar-footer"
      className="flex shrink-0 items-center justify-between gap-2 px-3 py-2.5"
    >
      <span
        className="min-w-0 truncate text-[11px] font-medium tabular-nums text-ink-4"
        title={versionLabel}
      >
        {versionLabel}
      </span>
      <Link
        to={LICENSE_PATH}
        className="shrink-0 text-[11px] text-ink-4 underline-offset-2 hover:text-ink-2 hover:underline"
      >
        {t('common.sidebar.license')}
      </Link>
    </div>
  )
}

function SidebarBody({
  collapsed,
  onNavigate,
}: {
  collapsed: boolean
  onNavigate?: () => void
}) {
  const { t } = useTranslation()
  return (
    <>
      {/* 品牌：仅 logo + Beacon，无底部分割线 */}
      <div
        className={[
          'flex items-center',
          collapsed ? 'justify-center px-0 py-[14px]' : 'gap-2.5 px-[18px] py-[14px]',
        ].join(' ')}
      >
        <img alt="Beacon" className="size-[30px] shrink-0" src="/logo.svg" />
        {!collapsed && (
          <div className="min-w-0 text-[15px] font-semibold tracking-[0.2px] text-ink-1">Beacon</div>
        )}
      </div>

      <nav
        aria-label={t('common.consoleName')}
        className="scrollbar-hide flex-1 overflow-y-auto px-2.5 py-2.5"
      >
        <div className="grid gap-px">
          <SidebarNavItem collapsed={collapsed} page={DASHBOARD_PAGE} onNavigate={onNavigate} />
        </div>
        {NAV_GROUPS.map((group) => (
          <div key={group.titleKey}>
            {/* 折叠时不再画分隔线，与侧栏视觉打通 */}
            {!collapsed && (
              <div className="px-2.5 pt-3 pb-1.5 text-[10.5px] font-semibold tracking-[0.6px] text-ink-4 uppercase">
                {t(group.titleKey)}
              </div>
            )}
            <div className={['grid gap-px', collapsed ? 'mt-1' : ''].join(' ')}>
              {group.pages.map((page) => (
                <SidebarNavItem
                  collapsed={collapsed}
                  key={page.path}
                  page={page}
                  onNavigate={onNavigate}
                />
              ))}
            </div>
          </div>
        ))}
      </nav>

      <SidebarFooter collapsed={collapsed} />
    </>
  )
}

export default function Sidebar() {
  const sidebarCollapsed = useShellStore((state) => state.sidebarCollapsed)
  const mobileNavOpen = useShellStore((state) => state.mobileNavOpen)
  const closeMobileNav = useShellStore((state) => state.closeMobileNav)
  const desktopWidth = sidebarCollapsed ? SIDEBAR_WIDTH_COLLAPSED : SIDEBAR_WIDTH_EXPANDED

  return (
    <>
      {/* 桌面：无右边框，与内容区视觉打通；折叠钮由 app-shell 贴右缘 */}
      <aside
        data-collapsed={sidebarCollapsed ? 'true' : 'false'}
        data-slot="desktop-sidebar"
        className="fixed inset-y-0 left-0 z-20 hidden flex-col bg-sidebar transition-[width] duration-200 ease-out md:flex"
        style={{ width: desktopWidth }}
      >
        <SidebarBody collapsed={sidebarCollapsed} />
      </aside>

      {/* 小屏抽屉：始终展开文案，不走图标轨 */}
      <aside
        data-slot="mobile-sidebar"
        data-open={mobileNavOpen ? 'true' : 'false'}
        className={[
          'fixed inset-y-0 left-0 z-40 flex w-[232px] flex-col bg-sidebar shadow-card',
          'transition-transform duration-200 ease-out md:hidden',
          mobileNavOpen ? 'translate-x-0' : '-translate-x-full',
        ].join(' ')}
      >
        <SidebarBody collapsed={false} onNavigate={closeMobileNav} />
      </aside>
    </>
  )
}
