// 侧栏导航（FR-186 / FR-189）：
// 桌面：展开 208px / 折叠 56px 图标轨，宽度与主区同步动画。
// 小屏：默认隐藏；打开时为全宽文案抽屉（不走图标轨）。
// 品牌展示 logo、Beacon 与控制面状态；无内部分割线；底栏左版本号、右开源许可。
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link, NavLink } from 'react-router-dom'
import { ChevronLeft, ChevronRight } from 'lucide-react'

import { fetchSystemStatus } from '../api/system'
import { DASHBOARD_PAGE, NAV_GROUPS, type NavPage } from '../routes'
import { useShellStore } from '../store'

/** 展开宽 / 折叠图标轨宽（不含左侧外沿） */
export const SIDEBAR_WIDTH_EXPANDED = 208
export const SIDEBAR_WIDTH_COLLAPSED = 56
export const SIDEBAR_GUTTER = 12

export const SIDEBAR_MOTION = 'duration-[400ms] ease-[cubic-bezier(0.22,1,0.36,1)]'

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
          'group relative flex items-center overflow-hidden whitespace-nowrap rounded-lg text-[12.5px] transition-[padding,background-color]',
          SIDEBAR_MOTION,
          collapsed ? 'delay-[50ms] motion-reduce:delay-0' : 'delay-0',
          collapsed ? 'justify-center px-0 py-2' : 'justify-start py-[7px] pl-9 pr-2.5',
          isActive
            ? 'bg-sidebar-accent font-semibold text-sidebar-accent-foreground'
            : 'text-ink-2 hover:bg-surface-2 hover:text-ink-1',
        ].join(' ')
      }
    >
      {({ isActive }) => (
        <>
          <page.icon
            className={[
              'absolute top-1/2 shrink-0 -translate-y-1/2 transition-[left,transform,width,height]',
              SIDEBAR_MOTION,
              collapsed ? 'delay-[50ms] motion-reduce:delay-0' : 'delay-0',
              collapsed
                ? 'left-1/2 size-[18px] -translate-x-1/2'
                : 'left-2.5 size-4 translate-x-0',
              isActive ? 'text-brand' : 'text-ink-4',
            ].join(' ')}
          />
          <span
            className={[
              'flex min-w-0 flex-1 items-center gap-1 overflow-hidden whitespace-nowrap transition-[max-width,opacity,transform]',
              SIDEBAR_MOTION,
              collapsed
                ? 'max-w-0 -translate-x-1 opacity-0 delay-0'
                : 'max-w-[180px] translate-x-0 opacity-100 delay-[50ms]',
            ].join(' ')}
          >
            <span className="truncate">{label}</span>
            {page.Badge && <page.Badge />}
          </span>
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
  showToggle = false,
}: {
  collapsed: boolean
  onNavigate?: () => void
  // 折叠按钮只在桌面侧栏渲染：小屏抽屉本身就是临时覆盖层，没有"折叠轨"概念
  showToggle?: boolean
}) {
  const { t } = useTranslation()
  // 折叠按钮已移入侧栏内部，直接读 store 的切换动作（桌面与抽屉共用同一份状态）
  const toggleSidebar = useShellStore((state) => state.toggleSidebar)

  return (
    <>
      {/* 品牌区：logo（右上角挂控制面状态点）+ Beacon 字样 + 折叠按钮。
          折叠按钮移入侧栏内部：
            · 展开态位于品牌行右端；
            · 收起态（图标轨）移到 logo 下方居中，避免 56px 宽度里横排挤爆。 */}
      <div
        className={[
          'flex flex-col transition-[padding]',
          SIDEBAR_MOTION,
          collapsed ? 'px-0 pt-[14px] pb-1' : 'px-[16px] pt-[14px] pb-2',
        ].join(' ')}
      >
        <div
          className={[
            'flex items-center transition-[gap]',
            SIDEBAR_MOTION,
            collapsed ? 'justify-center gap-0' : 'gap-2',
          ].join(' ')}
        >
          {/* logo（控制面状态点已上移到页眉指标区最前，不再贴在 logo 上） */}
          <span className="relative inline-flex shrink-0">
            <img alt="Beacon" className="size-[30px]" src="/logo.svg" />
          </span>
          {/* 收起态该文字宽度归零：必须同时抵消 flex gap，否则零宽节点仍会占一份间距把行撑开 */}
          <div
            className={[
              'overflow-hidden whitespace-nowrap text-[15px] font-semibold tracking-[0.2px] text-ink-1 transition-[max-width,opacity,transform]',
              SIDEBAR_MOTION,
              collapsed
                ? 'max-w-0 translate-x-1 opacity-0 delay-0'
                : 'max-w-[110px] translate-x-0 opacity-100 delay-[50ms]',
            ].join(' ')}
          >
            Beacon
          </div>
          {/* 展开态折叠按钮：品牌行右端，纯图标按钮 */}
          {showToggle && !collapsed && (
            <button
              aria-label={t('common.sidebar.collapse')}
              title={t('common.sidebar.collapse')}
              type="button"
              onClick={toggleSidebar}
              className="ml-auto grid size-6 shrink-0 place-items-center rounded-md text-ink-4 transition-colors hover:bg-muted hover:text-ink-1"
            >
              <ChevronLeft className="size-4" />
            </button>
          )}
        </div>
        {/* 收起态折叠按钮：logo 下方居中 */}
        {showToggle && collapsed && (
          <button
            aria-label={t('common.sidebar.expand')}
            title={t('common.sidebar.expand')}
            type="button"
            onClick={toggleSidebar}
            className={[
              'mx-auto mt-1.5 grid size-6 place-items-center rounded-md text-ink-4 transition-colors hover:bg-muted hover:text-ink-1',
              'delay-[50ms] motion-reduce:delay-0',
            ].join(' ')}
          >
            <ChevronRight className="size-4" />
          </button>
        )}
      </div>

      {/* 导航区可滚动：底部加一层渐隐遮罩，让"还有更多"看起来是刻意的淡出，
          而不是硬生生把某个菜单项切一半（折叠态下侧栏很窄，硬切尤其显脏）。 */}
      <nav
        aria-label={t('common.consoleName')}
        className="scrollbar-hide flex-1 overflow-y-auto px-2.5 py-2.5 [mask-image:linear-gradient(to_bottom,black_calc(100%-28px),transparent)]"
      >
        <div className="grid gap-px">
          <SidebarNavItem collapsed={collapsed} page={DASHBOARD_PAGE} onNavigate={onNavigate} />
        </div>
        {NAV_GROUPS.map((group) => (
          <div key={group.titleKey}>
            {/* 折叠时不再画分隔线，与侧栏视觉打通 */}
            {!collapsed && (
              <div className="px-2.5 pt-3.5 pb-1.5 text-[11px] font-semibold tracking-[0.04em] text-ink-3">
                {t(group.titleKey)}
              </div>
            )}
            <div className={['grid gap-0.5', collapsed ? 'mt-1.5' : ''].join(' ')}>
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
      {/* 桌面：四周留白的悬浮圆角卡片（与页眉各自独立，不做连体外壳） */}
      <aside
        data-collapsed={sidebarCollapsed ? 'true' : 'false'}
        data-slot="desktop-sidebar"
        id="desktop-sidebar"
        className={[
          'fixed z-20 hidden flex-col overflow-hidden rounded-2xl border border-border/60',
          'bg-sidebar/95 shadow-pop backdrop-blur-sm transition-[width]',
          SIDEBAR_MOTION,
          sidebarCollapsed ? 'delay-[50ms] motion-reduce:delay-0' : 'delay-0',
          'md:flex',
        ].join(' ')}
        style={{
          width: desktopWidth,
          left: SIDEBAR_GUTTER,
          top: SIDEBAR_GUTTER,
          bottom: SIDEBAR_GUTTER,
        }}
      >
        <SidebarBody collapsed={sidebarCollapsed} showToggle />
      </aside>

      {/* 小屏抽屉：保持原有悬浮卡片形态，不并入通栏外壳。 */}
      <aside
        data-slot="mobile-sidebar"
        data-open={mobileNavOpen ? 'true' : 'false'}
        className={[
          'fixed z-40 flex flex-col overflow-hidden rounded-2xl border border-border/60',
          'bg-sidebar/95 shadow-pop backdrop-blur-sm transition-transform',
          SIDEBAR_MOTION,
          'md:hidden',
        ].join(' ')}
        style={{
          width: `min(${String(SIDEBAR_WIDTH_EXPANDED)}px, calc(100vw - ${String(SIDEBAR_GUTTER * 2)}px))`,
          left: SIDEBAR_GUTTER,
          top: SIDEBAR_GUTTER,
          bottom: SIDEBAR_GUTTER,
          transform: mobileNavOpen
            ? 'translateX(0)'
            : `translateX(calc(-100% - ${String(SIDEBAR_GUTTER)}px))`,
        }}
      >
        <SidebarBody collapsed={false} onNavigate={closeMobileNav} />
      </aside>
    </>

  )
}
