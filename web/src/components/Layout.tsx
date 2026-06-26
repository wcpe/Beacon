// 顶层布局：侧边导航 + 当前登录身份 + 登出 + 主内容区。
// 操作者身份由登录令牌决定（FR-11），写操作 operator 以认证身份为准，无需手填。

import { useEffect, useState } from 'react'
import { NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { clearAuth, useAuth } from '@/state/auth'
import { logout } from '@/api/client'
import { Button } from '@/components/ui/button'
import SystemHeader from '@/components/SystemHeader'
import PageHeader, { PageHeaderProvider } from '@/components/PageHeader'
import CommandPalette from '@/components/CommandPalette'
import { useConnectionStatus } from '@/hooks/useConnectionStatus'
import { NAV_GROUPS } from '@/lib/navModel'
import { cn } from '@/lib/utils'

export default function Layout() {
  const { t } = useTranslation()
  const { operator } = useAuth()
  const navigate = useNavigate()
  // 路由路径用作内容淡入的 key：切页时重挂载触发 animate-in fade-in，过渡不生硬（复用 tw-animate-css，无新依赖）
  const location = useLocation()
  // 控制面连接状态（FR-78）：断开时弹横幅、小灯转红；恢复时自动重连并刷新数据。
  const { status: connectionStatus } = useConnectionStatus()
  // 全局命令面板开合（FR-83）：Ctrl/Cmd+K 唤起、页眉搜索入口同开同一面板。
  const [paletteOpen, setPaletteOpen] = useState(false)

  // 全局 Ctrl/Cmd+K 监听：任意页面（含输入框聚焦时）皆可唤起，面板为模态覆盖；
  // 与配置页 Ctrl+S 保存（FR-75）不同键、不冲突。
  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if ((e.ctrlKey || e.metaKey) && (e.key === 'k' || e.key === 'K')) {
        e.preventDefault()
        setPaletteOpen((v) => !v)
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [])

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
    // 外层改纵向：最上是「贯穿整宽顶栏」（品牌区 | 控制面状态条），其下是 flex-1 的「侧栏 | 右列」（FR-105 真机打磨）。
    <div className="flex h-screen flex-col overflow-hidden bg-background text-foreground">
      {/* 整宽顶栏（~40px）：左品牌区（宽 = 侧栏宽 w-56，右边框接侧栏竖线）+ 右控制面状态条（占满剩余宽度） */}
      <header className="flex h-10 shrink-0 items-stretch border-b bg-background">
        {/* 左侧品牌区（宽 = 侧栏宽、与下方侧栏对齐）：整块可点跳可观测看板（/dashboard），保留连接状态小灯（FR-78） */}
        <button
          type="button"
          onClick={() => navigate('/dashboard')}
          aria-label={t('layout.brandToDashboard')}
          className="flex w-56 shrink-0 items-center gap-2 border-r px-5 text-left text-sm font-semibold transition-colors hover:bg-sidebar-accent/40"
        >
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
        </button>
        {/* 右侧控制面状态条（FR-33）：占满品牌区之外的剩余宽度；SystemHeader 只渲染内容，外壳由本顶栏统一。
            搜索入口已从侧栏移至此页眉右上角（FR-83），点开同一命令面板浮层。 */}
        <div className="flex min-w-0 flex-1 items-center px-6">
          <SystemHeader onOpenSearch={() => setPaletteOpen(true)} />
        </div>
      </header>
      {/* 顶栏之下：侧栏（导航 + 操作人）| 右列（断线横幅 + 第二层 PageHeader + 主内容） */}
      <div className="flex min-h-0 flex-1">
      {/* 侧栏整列撑满剩余高度并裁剪溢出：顶部品牌已上移顶栏，本侧栏顶部仅导航、底部操作区冻结，仅中间导航滚动。 */}
      <aside className="flex w-56 shrink-0 flex-col overflow-hidden border-r bg-sidebar text-sidebar-foreground">
        {/* 侧栏 5 组分组常驻（FR-93，方案 A）：分区标题（不可点、不折叠）+ 其下叶子常驻显示，
            每个叶子 = lucide 图标 + 文案；不再用 details/summary 折叠，无展开态偏好。
            中间导航为侧栏唯一可滚区（flex-1 overflow-y-auto），顶/底冻结。 */}
        <nav className="scrollbar-hide flex flex-1 flex-col gap-3 overflow-y-auto p-3">
          {NAV_GROUPS.map((group) => (
            <div key={group.id} className="flex flex-col gap-0.5">
              {/* 分区标题：小号弱色、无 chevron、不可点击、不折叠，仅作分组层级标识 */}
              <div className="px-3 pb-1 text-xs font-semibold text-muted-foreground select-none">
                {t(group.labelKey)}
              </div>
              {group.leaves.map((leaf) => {
                const Icon = leaf.icon
                return (
                  <NavLink
                    key={leaf.to}
                    to={leaf.to}
                    // 拍平为独立页后逐一精确高亮（ADR-0048）：end 杜绝 /system 前缀误命中 /system/version 等同组兄弟
                    end
                    className={({ isActive }) =>
                      cn(
                        'flex items-center gap-2 rounded-md px-3 py-1.5 text-sm transition-colors',
                        isActive
                          ? 'bg-sidebar-accent font-medium text-sidebar-accent-foreground'
                          : 'text-sidebar-foreground/70 hover:bg-sidebar-accent/50 hover:text-sidebar-accent-foreground',
                      )
                    }
                  >
                    <Icon aria-hidden className="size-4 shrink-0" />
                    <span>{t(leaf.labelKey)}</span>
                  </NavLink>
                )
              })}
            </div>
          ))}
        </nav>
        {/* 底部「当前操作人 + 登出」（冻结，不随导航滚动） */}
        <div className="shrink-0 border-t p-4">
          <div className="text-xs text-muted-foreground">{t('layout.currentOperator')}</div>
          <div className="mb-2 mt-0.5 break-all text-sm font-medium">{operator || '-'}</div>
          <Button variant="outline" size="sm" className="w-full" onClick={onLogout}>
            {t('layout.logout')}
          </Button>
        </div>
      </aside>
      {/* 右列：PageHeaderProvider 包裹，使各页 usePageHeader 注入的第二层页眉配置对 PageHeader 生效（FR-105）。 */}
      <PageHeaderProvider>
        <div className="flex min-w-0 flex-1 flex-col overflow-hidden">
        {/* 控制面连接中断横幅（FR-78）：仅断开时显示，恢复后自动消失；治控制面重部时 UI 静默掉线。
            真机打磨后放右列内容区顶部（顶栏之下、第二层页眉之上），断线行为不回归。 */}
        {connectionStatus === 'offline' && (
          <div
            role="alert"
            className="flex shrink-0 items-center gap-2 border-b border-red-600/40 bg-red-600/10 px-6 py-2 text-sm font-medium text-red-700 dark:text-red-400"
          >
            <span aria-hidden className="inline-block h-2 w-2 animate-pulse rounded-full bg-red-600" />
            {t('connection.banner')}
          </div>
        )}
        {/* 第二层「页面头带」PageHeader（FR-105）：显当前页标题 + 环境槽（环境范围页）+ 主操作槽；
            第一层控制面状态条已上移至整宽顶栏（不再在此渲染）。 */}
        <PageHeader />
        {/* 主内容区纵向可滚动：普通堆叠页（看板/审计/实例等）内容超高时正常滚动；
            自管满屏页（配置/文件树/拓印）以 h-full + 内部滚动适配，不会触发此处滚动条。
            relative 必不可少：作为绝对定位后代（recharts 图表 / 状态瓷砖色条 / tooltip 等）的包含块，
            否则这些后代锚定到初始包含块（视口）、撑大整个文档导致连同侧栏页眉一起的窗口级滚动。 */}
        <main className="scrollbar-hide relative min-w-0 flex-1 overflow-y-auto p-6">
          {/* 按路由 key 重挂载并淡入：克制的内容淡入，让路由切换不生硬（tw-animate-css 的 animate-in fade-in） */}
          <div key={location.pathname} className="animate-in fade-in duration-200">
            <Outlet />
          </div>
        </main>
        </div>
      </PageHeaderProvider>
      </div>
      {/* 全局命令面板（FR-83）：受 Layout 持有的开合态控制，Ctrl/Cmd+K 或页眉入口唤起 */}
      <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
    </div>
  )
}
