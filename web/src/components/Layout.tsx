// 顶层布局：侧边导航 + 当前登录身份 + 登出 + 主内容区。
// 操作者身份由登录令牌决定（FR-11），写操作 operator 以认证身份为准，无需手填。

import { useEffect, useRef, useState } from 'react'
import { NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ChevronsLeft, ChevronsRight } from 'lucide-react'
import SystemHeader from '@/components/SystemHeader'
import PageHeader, { PageHeaderProvider } from '@/components/PageHeader'
import CommandPalette from '@/components/CommandPalette'
import { useConnectionStatus } from '@/hooks/useConnectionStatus'
import { useDocumentTitle } from '@/hooks/useDocumentTitle'
import { NAV_GROUPS, NAV_LEAVES } from '@/lib/navModel'
import { toggleSidebar, useUiState } from '@/state/ui'
import { cn } from '@/lib/utils'

export default function Layout() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  // 路由路径用作内容淡入的 key：切页时重挂载触发 animate-in fade-in，过渡不生硬（复用 tw-animate-css，无新依赖）
  const location = useLocation()
  // 控制面连接状态（FR-78）：断开时弹横幅、小灯转红；恢复时自动重连并刷新数据。
  const { status: connectionStatus } = useConnectionStatus()
  // 全局命令面板开合（FR-83）：Ctrl/Cmd+K 唤起、页眉搜索入口同开同一面板。
  const [paletteOpen, setPaletteOpen] = useState(false)
  // 侧栏折叠态（改进 1：可折叠图标条）：折叠=窄图标条 w-14（仅图标+tooltip）、展开=图标+文案 w-56；
  // 折叠态由 state/ui 持久化到 localStorage，品牌区与侧栏同宽联动。
  const { sidebarCollapsed } = useUiState()

  // 动态标签标题（FR-123）：按当前路由匹配导航叶子（先精确、后前缀，使 /configs/:id 等子路由归其父页），
  // 设 document.title = 「<页名> - Beacon」；未知路由回退「Beacon」。
  const titleLeaf =
    NAV_LEAVES.find((l) => l.to === location.pathname) ??
    NAV_LEAVES.find((l) => location.pathname.startsWith(l.to + '/'))
  useDocumentTitle(titleLeaf ? t(titleLeaf.labelKey) : undefined)

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

  // 品牌区单击 / 双击区分（FR-121）：单击跳看板、双击折叠/展开侧栏。
  // 单击延迟 ~200ms 执行，留出双击取消窗口；双击命中则清掉待执行的单击跳转。
  const brandClickTimer = useRef<number | null>(null)
  useEffect(() => {
    // 卸载时清理待执行的单击定时器，避免卸载后触发跳转
    return () => {
      if (brandClickTimer.current !== null) window.clearTimeout(brandClickTimer.current)
    }
  }, [])

  function handleBrandClick() {
    if (brandClickTimer.current !== null) window.clearTimeout(brandClickTimer.current)
    brandClickTimer.current = window.setTimeout(() => {
      brandClickTimer.current = null
      navigate('/dashboard')
    }, 200)
  }

  function handleBrandDoubleClick() {
    // 双击：取消待执行的单击跳转，改为切换侧栏折叠/展开
    if (brandClickTimer.current !== null) {
      window.clearTimeout(brandClickTimer.current)
      brandClickTimer.current = null
    }
    toggleSidebar()
  }

  return (
    // 外层改纵向：最上是「贯穿整宽顶栏」（品牌区 | 控制面状态条），其下是 flex-1 的「侧栏 | 右列」（FR-105 真机打磨）。
    <div className="flex h-screen flex-col overflow-hidden bg-background text-foreground">
      {/* 整宽顶栏（~40px）：左品牌区（宽 = 侧栏宽 w-56，右边框接侧栏竖线）+ 右控制面状态条（占满剩余宽度） */}
      <header className="flex h-10 shrink-0 items-stretch border-b bg-background">
        {/* 左侧品牌区（宽 = 侧栏宽、与下方侧栏对齐）：logo（灯塔/信标矢量图 FR-123）+ 「Beacon」+ 版本徽章。
            FR-121：logo+文案 单击跳可观测看板、双击切换侧栏折叠/展开；版本徽章在 logo 右侧（展开态显示）。
            FR-123：原连接小灯（FR-78）移除——连接态仍由顶栏右侧「已连接」药丸显示，不在品牌区重复。
            宽度随折叠态联动（折叠 w-14 仅居中显 logo、展开 w-56 显 logo+文案+版本）。 */}
        <div
          className={cn(
            'flex shrink-0 items-center border-r',
            sidebarCollapsed ? 'w-14 justify-center px-0' : 'w-56 gap-2 px-3',
          )}
        >
          <button
            type="button"
            onClick={handleBrandClick}
            onDoubleClick={handleBrandDoubleClick}
            aria-label={t('layout.brandToDashboard')}
            title={t('layout.brandHint')}
            className="flex min-w-0 items-center gap-2 rounded-md px-2 py-1 text-left text-sm font-semibold transition-colors hover:bg-sidebar-accent/40"
          >
            {/* 品牌 logo（信标方块矢量图，FR-123/FR-126 放大）：折叠 / 展开态均显示，作 logo 点 */}
            <img src="/logo.svg" alt="" className="size-7 shrink-0" />
            {/* 折叠态隐藏品牌文案，仅留 logo；FR-126 名放大 text-base */}
            {!sidebarCollapsed && <span className="truncate text-base font-semibold">{t('app.name')}</span>}
          </button>
          {/* FR-126：版本徽章从品牌区移到顶栏「已连接」药丸之后（见 SystemHeader），品牌区不再含版本 */}
        </div>
        {/* 右侧控制面状态条（FR-33）：占满品牌区之外的剩余宽度；SystemHeader 只渲染内容，外壳由本顶栏统一。
            搜索入口已从侧栏移至此页眉右上角（FR-83），点开同一命令面板浮层。 */}
        <div className="flex min-w-0 flex-1 items-center px-6">
          <SystemHeader onOpenSearch={() => setPaletteOpen(true)} />
        </div>
      </header>
      {/* 顶栏之下：侧栏（导航 + 操作人）| 右列（断线横幅 + 第二层 PageHeader + 主内容） */}
      <div className="flex min-h-0 flex-1">
      {/* 侧栏整列撑满剩余高度并裁剪溢出：顶部品牌已上移顶栏，本侧栏顶部仅导航、底部操作区冻结，仅中间导航滚动。
          改进 1：宽度随折叠态联动（折叠 w-14 仅图标条、展开 w-56 图标+文案），保持 FR-93/ADR-0048 扁平 IA（5 组叶子结构不变，仅折叠宽度不隐藏层级）。 */}
      <aside
        className={cn(
          'flex shrink-0 flex-col overflow-hidden border-r bg-sidebar text-sidebar-foreground transition-all',
          sidebarCollapsed ? 'w-14' : 'w-56',
        )}
      >
        {/* 侧栏 5 组分组常驻（FR-93，方案 A）：分区标题（不可点、不折叠）+ 其下叶子常驻显示，
            每个叶子 = lucide 图标 + 文案；不再用 details/summary 折叠，无展开态偏好。
            中间导航为侧栏唯一可滚区（flex-1 overflow-y-auto），顶/底冻结。
            折叠态：隐藏文案与分区标题文字（标题降为细分隔线），每个图标 hover 经 title 显 tooltip。 */}
        <nav className="scrollbar-hide flex flex-1 flex-col gap-3 overflow-y-auto p-2">
          {NAV_GROUPS.map((group) => (
            <div key={group.id} className="flex flex-col gap-0.5">
              {sidebarCollapsed ? (
                // 折叠态：分区标题降为一条细分隔线（仅作分组层级标识、不显文字）
                <div className="mx-2 mb-1 h-px bg-border/60 first:hidden" aria-hidden />
              ) : (
                // 分区标题：小号弱色、无 chevron、不可点击、不折叠，仅作分组层级标识
                <div className="px-3 pb-1 text-xs font-semibold text-muted-foreground select-none">
                  {t(group.labelKey)}
                </div>
              )}
              {group.leaves.map((leaf) => {
                const Icon = leaf.icon
                return (
                  <NavLink
                    key={leaf.to}
                    to={leaf.to}
                    // 拍平为独立页后逐一精确高亮（ADR-0048）：end 杜绝 /system 前缀误命中 /system/version 等同组兄弟
                    end
                    // 折叠态：hover 经原生 title 显 tooltip（文案=该叶子名）
                    title={sidebarCollapsed ? t(leaf.labelKey) : undefined}
                    className={({ isActive }) =>
                      cn(
                        'flex items-center gap-2 rounded-md py-1.5 text-sm transition-colors',
                        sidebarCollapsed ? 'justify-center px-0' : 'px-3',
                        isActive
                          ? 'bg-sidebar-accent font-medium text-sidebar-accent-foreground'
                          : 'text-sidebar-foreground/70 hover:bg-sidebar-accent/50 hover:text-sidebar-accent-foreground',
                      )
                    }
                  >
                    <Icon aria-hidden className="size-4 shrink-0" />
                    {!sidebarCollapsed && <span>{t(leaf.labelKey)}</span>}
                  </NavLink>
                )
              })}
            </div>
          ))}
        </nav>
        {/* 底部「开源协议 + 折叠/展开按钮」（FR-121，冻结，不随导航滚动）。
            当前操作人 / 登出已移至顶栏右上角账户菜单（见 OperatorMenu）。
            展开态：开源协议链接（左）+ 折叠按钮（右）一行；折叠态：仅居中展开图标按钮。 */}
        <div className="shrink-0 border-t p-2">
          {sidebarCollapsed ? (
            <button
              type="button"
              onClick={toggleSidebar}
              aria-label={t('layout.sidebarExpand')}
              title={t('layout.sidebarExpand')}
              className="flex w-full justify-center rounded-md py-1.5 text-sidebar-foreground/70 transition-colors hover:bg-sidebar-accent/50 hover:text-sidebar-accent-foreground"
            >
              <ChevronsRight aria-hidden className="size-4 shrink-0" />
            </button>
          ) : (
            <div className="flex items-center justify-between gap-2 px-1">
              {/* 开源协议：新标签打开仓库 LICENSE（MIT） */}
              <a
                href="https://github.com/wcpe/Beacon/blob/master/LICENSE"
                target="_blank"
                rel="noreferrer"
                className="text-xs text-sidebar-foreground/60 transition-colors hover:text-sidebar-accent-foreground hover:underline"
              >
                {t('layout.openSourceLicense')}
              </a>
              {/* 折叠按钮 */}
              <button
                type="button"
                onClick={toggleSidebar}
                aria-label={t('layout.sidebarCollapse')}
                title={t('layout.sidebarCollapse')}
                className="flex items-center justify-center rounded-md p-1.5 text-sidebar-foreground/70 transition-colors hover:bg-sidebar-accent/50 hover:text-sidebar-accent-foreground"
              >
                <ChevronsLeft aria-hidden className="size-4 shrink-0" />
              </button>
            </div>
          )}
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
          {/* 按路由 key 重挂载并淡入：克制的内容淡入，让路由切换不生硬（tw-animate-css 的 animate-in fade-in）。
              h-full 必不可少：给「自管满屏页」（配置工作台 / 拓印 / 文件预览，根用 h-full + overflow-hidden）
              一个有定值高度的父级，其 h-full 才能解析；否则父级高度为 auto，满屏页塌成内容高、
              内部面板拿不到固定高度，转而触发 main 的窗口级滚动。普通堆叠页内容超高时仍照常由 main 滚动。 */}
          <div key={location.pathname} className="h-full animate-in fade-in duration-200">
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
