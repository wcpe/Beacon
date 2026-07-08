// 侧栏导航（对齐 B 版明亮 SaaS）：品牌区（靛蓝渐变方块 logo）+ 顶层运维总览 + 四大域分组
// （集群 / 可观测 / 交付 / 系统，10.5px 大写微标题）+ 底部操作人。
// active 项为浅靛蓝底 + 靛蓝文字；整栏折叠沿用 Zustand 的 sidebarCollapsed，由页眉按钮切换。
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
          'group flex items-center gap-2.5 rounded-lg px-2.5 py-[7px] text-[12.5px] transition-colors',
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
          <span>{t(page.titleKey)}</span>
        </>
      )}
    </NavLink>
  )
}

export default function Sidebar() {
  const { t } = useTranslation()
  const sidebarCollapsed = useShellStore((state) => state.sidebarCollapsed)
  return (
    <aside
      className={[
        'fixed inset-y-0 left-0 hidden w-[232px] flex-col border-r border-border bg-sidebar',
        sidebarCollapsed ? 'md:hidden' : 'md:flex',
      ].join(' ')}
    >
      {/* 品牌区：靛蓝渐变方块灯塔 logo + 名称 + 环境副标题 */}
      <div className="flex items-center gap-2.5 border-b border-border px-[18px] py-[14px]">
        <div className="grid size-[30px] place-items-center rounded-lg bg-gradient-to-br from-brand to-[oklch(0.62_0.19_282)] text-white shadow-[0_2px_6px_rgba(79,70,229,0.35)]">
          <svg
            width="17"
            height="17"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden
          >
            <path d="M12 2v3" />
            <path d="m4.5 21 3-9m12 9-3-9" />
            <path d="M8 8h8l1 4H7z" />
            <path d="M9 12v9m6-9v9" />
            <path d="M8 21h8" />
          </svg>
        </div>
        <div className="min-w-0">
          <div className="text-[15px] font-semibold tracking-[0.2px] text-ink-1">Beacon</div>
          <div className="truncate text-[11px] text-ink-4">{t('common.consoleName')}</div>
        </div>
      </div>

      {/* 导航：顶层运维总览 + 四大域分组 */}
      <nav className="scrollbar-hide flex-1 overflow-y-auto px-2.5 py-2.5">
        <div className="grid gap-px">
          <SidebarNavItem page={DASHBOARD_PAGE} />
        </div>
        {NAV_GROUPS.map((group) => (
          <div key={group.titleKey}>
            <div className="px-2.5 pt-3 pb-1.5 text-[10.5px] font-semibold tracking-[0.6px] text-ink-4 uppercase">
              {t(group.titleKey)}
            </div>
            <div className="grid gap-px">
              {group.pages.map((page) => (
                <SidebarNavItem key={page.path} page={page} />
              ))}
            </div>
          </div>
        ))}
      </nav>

      {/* 底部操作人 */}
      <div className="flex items-center gap-2.5 border-t border-border px-3 py-2.5">
        <div className="grid size-7 place-items-center rounded-full bg-gradient-to-br from-[oklch(0.62_0.19_282)] to-[oklch(0.62_0.2_305)] text-[11px] font-bold text-white">
          运
        </div>
        <div className="min-w-0">
          <div className="truncate text-xs font-semibold text-ink-1">admin</div>
          <div className="truncate text-[10.5px] text-ink-4">{t('common.superAdmin')}</div>
        </div>
      </div>
    </aside>
  )
}
