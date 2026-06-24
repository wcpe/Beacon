// 顶层布局：侧边导航 + 当前登录身份 + 登出 + 主内容区。
// 操作者身份由登录令牌决定（FR-11），写操作 operator 以认证身份为准，无需手填。

import { NavLink, Outlet, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { clearAuth, useAuth } from '@/state/auth'
import { logout } from '@/api/client'
import { Button } from '@/components/ui/button'
import SystemHeader from '@/components/SystemHeader'
import { useConnectionStatus } from '@/hooks/useConnectionStatus'
import { cn } from '@/lib/utils'

// 导航项定义（路径 + i18n key）
const NAV_ITEMS: Array<{ to: string; labelKey: string }> = [
  { to: '/dashboard', labelKey: 'nav.dashboard' },
  { to: '/configs', labelKey: 'nav.configs' },
  { to: '/file-preview', labelKey: 'nav.filePreview' },
  { to: '/imprint', labelKey: 'nav.imprint' },
  { to: '/reverse-fetch', labelKey: 'nav.reverseFetchTask' },
  { to: '/servers', labelKey: 'nav.servers' },
  { to: '/topology', labelKey: 'nav.topology' },
  { to: '/zones', labelKey: 'nav.zones' },
  { to: '/audits', labelKey: 'nav.audits' },
  { to: '/service-analysis', labelKey: 'nav.serviceAnalysis' },
  { to: '/api-keys', labelKey: 'nav.apiKeys' },
  { to: '/namespaces', labelKey: 'nav.namespaces' },
  { to: '/settings', labelKey: 'nav.settings' },
  { to: '/system', labelKey: 'nav.systemObservability' },
]

export default function Layout() {
  const { t } = useTranslation()
  const { operator } = useAuth()
  const navigate = useNavigate()
  // 控制面连接状态（FR-78）：断开时弹横幅、小灯转红；恢复时自动重连并刷新数据。
  const { status: connectionStatus } = useConnectionStatus()

  async function onLogout() {
    // 先请求后端记一条登出审计（需当前令牌）；无论成败都清本地登录态并跳登录——登出绝不被阻断。
    try {
      await logout()
    } catch {
      // 令牌已过期等场景审计失败，忽略：登出是本地动作，不依赖后端成功
    }
    clearAuth()
    navigate('/login', { replace: true })
  }

  return (
    <div className="flex h-screen overflow-hidden bg-background text-foreground">
      <aside className="flex w-56 shrink-0 flex-col border-r bg-sidebar text-sidebar-foreground overflow-y-auto">
        <div className="flex items-center gap-2 border-b px-5 py-4 text-base font-semibold">
          {/* 全局连接状态小灯（FR-78）：绿=已连接、红=已断开、灰=连接中 */}
          <span
            aria-label={t(`connection.${connectionStatus}`)}
            title={t(`connection.${connectionStatus}`)}
            className={cn(
              'inline-block h-2 w-2 shrink-0 rounded-full',
              connectionStatus === 'online'
                ? 'bg-green-600'
                : connectionStatus === 'offline'
                  ? 'bg-red-600'
                  : 'bg-muted-foreground',
            )}
          />
          <span>{t('app.brand')}</span>
        </div>
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
              {t(item.labelKey)}
            </NavLink>
          ))}
        </nav>
        <div className="border-t p-4">
          <div className="text-xs text-muted-foreground">{t('layout.currentOperator')}</div>
          <div className="mb-2 mt-0.5 break-all text-sm font-medium">{operator || '-'}</div>
          <Button variant="outline" size="sm" className="w-full" onClick={onLogout}>
            {t('layout.logout')}
          </Button>
        </div>
      </aside>
      <div className="flex min-w-0 flex-1 flex-col overflow-hidden">
        {/* 控制面连接中断横幅（FR-78）：仅断开时显示，恢复后自动消失；治控制面重部时 UI 静默掉线 */}
        {connectionStatus === 'offline' && (
          <div
            role="alert"
            className="flex shrink-0 items-center gap-2 border-b border-red-600/40 bg-red-600/10 px-6 py-2 text-sm font-medium text-red-700 dark:text-red-400"
          >
            <span aria-hidden className="inline-block h-2 w-2 animate-pulse rounded-full bg-red-600" />
            {t('connection.banner')}
          </div>
        )}
        {/* 控制面自身状态条（FR-33）：收进右侧主内容列顶部，不再压在侧边栏之上 */}
        <SystemHeader />
        {/* 主内容区纵向可滚动：普通堆叠页（看板/审计/实例等）内容超高时正常滚动；
            自管满屏页（配置/文件树/拓印）以 h-full + 内部滚动适配，不会触发此处滚动条 */}
        <main className="min-w-0 flex-1 overflow-y-auto p-6">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
