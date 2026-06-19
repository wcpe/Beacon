// 顶层布局：侧边导航 + 当前登录身份 + 登出 + 主内容区。
// 操作者身份由登录令牌决定（FR-11），写操作 operator 以认证身份为准，无需手填。

import { NavLink, Outlet, useNavigate } from 'react-router-dom'
import { clearAuth, useAuth } from '@/state/auth'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

// 导航项定义（路径 + 中文名）
const NAV_ITEMS: Array<{ to: string; label: string }> = [
  { to: '/dashboard', label: '可观测看板' },
  { to: '/configs', label: '配置中心' },
  { to: '/instances', label: '实例与健康' },
  { to: '/zones', label: 'zone 分配' },
  { to: '/audits', label: '审计日志' },
  { to: '/namespaces', label: '环境管理' },
]

export default function Layout() {
  const { operator } = useAuth()
  const navigate = useNavigate()

  function onLogout() {
    clearAuth()
    navigate('/login', { replace: true })
  }

  return (
    <div className="flex h-screen overflow-hidden bg-background text-foreground">
      <aside className="flex w-56 shrink-0 flex-col border-r bg-sidebar text-sidebar-foreground overflow-y-auto">
        <div className="border-b px-5 py-4 text-base font-semibold">Beacon 管理台</div>
        <nav className="flex flex-1 flex-col gap-1 p-3">
          {NAV_ITEMS.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              className={({ isActive }) =>
                cn(
                  'rounded-md px-3 py-2 text-sm transition-colors',
                  isActive
                    ? 'bg-sidebar-accent font-medium text-sidebar-accent-foreground'
                    : 'text-sidebar-foreground/70 hover:bg-sidebar-accent/60 hover:text-sidebar-accent-foreground',
                )
              }
            >
              {item.label}
            </NavLink>
          ))}
        </nav>
        <div className="border-t p-4">
          <div className="text-xs text-muted-foreground">当前操作人</div>
          <div className="mb-2 mt-0.5 break-all text-sm font-medium">{operator || '-'}</div>
          <Button variant="outline" size="sm" className="w-full" onClick={onLogout}>
            登出
          </Button>
        </div>
      </aside>
      <main className="min-w-0 flex-1 overflow-hidden p-6">
        <Outlet />
      </main>
    </div>
  )
}
