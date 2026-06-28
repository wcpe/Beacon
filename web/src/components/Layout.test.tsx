// Layout 布局单测（FR-33 页眉重定位 + 侧栏结构修复）：
// 锁定「品牌标题在侧边栏 → 控制面状态条收进右侧主内容列顶部（侧边栏之外、内容之上）」的结构关系，
// 并锁定「侧栏顶/底冻结、仅中间导航可滚 + 品牌整块可点跳可观测看板 + 搜索块已从侧栏移除」。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { SystemStatusView } from '@/api/types'

// mock 后端调用：SystemHeader 经 react-query 拉取自身状态 + FR-100 更新检查链路
vi.mock('@/api/client', () => ({
  systemStatus: vi.fn(),
  logout: vi.fn(),
  // FR-100：SystemHeader 的 useUpdateCheck 用到；默认无可用更新（无红点），不影响既有断言
  checkUpdate: vi.fn().mockResolvedValue({
    status: 'ok',
    currentVersion: 'v0.7.0',
    channel: 'stable',
    hasUpdate: false,
    isDevBuild: false,
    latestVersion: '',
    releaseNotes: '',
    releaseUrl: '',
    publishedAt: '',
    checkedAt: '',
    cacheExpiresAt: '',
  }),
  listSettings: vi.fn().mockResolvedValue([]),
  // 第二层页眉 PageHeader 在环境范围页（如 /servers）渲染 EnvSelector，其内部拉取环境列表（FR-105）
  listNamespaces: vi.fn().mockResolvedValue([]),
}))

// 监听跳转：品牌区点击应跳可观测看板（/dashboard）
const navigateSpy = vi.fn()
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom')
  return { ...actual, useNavigate: () => navigateSpy }
})

import Layout from './Layout'
import { systemStatus } from '@/api/client'
import { setSidebarCollapsed } from '@/state/ui'

// 健康样例：仅供 SystemHeader 渲染，断言不依赖具体数值
const STATUS: SystemStatusView = {
  version: 'v0.7.0',
  startedAt: '2026-06-20T08:00:00Z',
  uptimeSeconds: 60,
  db: { connected: true },
  onlineInstances: 1,
  samplerEnabled: true,
  runtime: { goroutines: 1, heapAlloc: 1024, heapSys: 2048 },
  cpuAvailable: true,
  cpuPercent: 1.2,
}

function renderLayout(initialPath = '/') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[initialPath]}>
        <Layout />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

// 改进 1：侧栏默认折叠（w-14、仅图标）。state/ui 的内存快照在模块加载时一次性读取，
// 故测试须经 setSidebarCollapsed 设置（同时更新内存快照 + localStorage），不能仅写 localStorage。
// 多数既有断言基于展开态结构（w-56、操作人信息块等），渲染前置展开态；折叠态另起一组断言锁定。

beforeEach(() => {
  localStorage.clear()
  // 既有断言面向展开态：默认置展开，折叠态测试自行覆盖
  setSidebarCollapsed(false)
  navigateSpy.mockReset()
  vi.mocked(systemStatus).mockResolvedValue(STATUS)
})

describe('Layout 顶栏化（FR-105 真机打磨：品牌上移整宽顶栏 + 状态条同栏）', () => {
  it('品牌标题渲染在整宽顶栏内（已上移，不在侧边栏内）', () => {
    renderLayout()
    const brand = screen.getByText('Beacon 管理台')
    // 品牌已上移至顶栏：不再落在侧边栏（aside）内，而在顶栏 header 内、宽度对齐侧栏（w-56）
    expect(brand.closest('aside')).toBeNull()
    const brandBtn = brand.closest('button')
    expect(brandBtn).not.toBeNull()
    // FR-121：宽度（w-56）移到品牌区容器（button 父级），button 自身为内容宽
    expect(brandBtn?.parentElement?.classList.contains('w-56')).toBe(true)
    expect(brandBtn?.closest('header')).not.toBeNull()
  })

  it('控制面状态条与品牌区同处整宽顶栏（不在侧边栏内）', async () => {
    renderLayout()
    const stateLabel = await screen.findByText('控制面状态')
    // 状态条不应落在侧边栏内
    expect(stateLabel.closest('aside')).toBeNull()
    // 状态条与品牌区共处同一顶栏 header
    const headerEl = stateLabel.closest('header')
    expect(headerEl).not.toBeNull()
    const brandBtn = screen.getByText('Beacon 管理台').closest('button')
    expect(brandBtn?.closest('header')).toBe(headerEl)
    // 顶栏 header 位于 <main> 之外（顶栏在侧栏 + 右列之上）
    const mainEl = document.querySelector('main')
    expect(mainEl).not.toBeNull()
    expect(headerEl?.contains(mainEl as Node)).toBe(false)
  })

  it('主内容区纵向可滚动（不裁剪超高内容，回归「滚动锁死」）', () => {
    renderLayout()
    // <main> 须为 overflow-y-auto 而非 overflow-hidden：
    // 普通堆叠页内容超过视口高度时应可滚动看全，不被裁在左上角。
    const mainEl = document.querySelector('main')
    expect(mainEl).not.toBeNull()
    expect(mainEl?.classList.contains('overflow-y-auto')).toBe(true)
    expect(mainEl?.classList.contains('overflow-hidden')).toBe(false)
  })

  it('主内容区为定位包含块（relative，防绝对定位后代撑出窗口级滚动）', () => {
    renderLayout()
    // <main> 须 position: relative：作为 recharts 图表 / 状态瓷砖色条 / tooltip 等绝对定位后代的包含块。
    // 缺失时这些后代锚定到视口、撑大文档高度，导致连同侧栏页眉一起的整窗口滚动（jsdom 无布局引擎，
    // 仅能锁定该结构性类名作回归护栏，真实滚动行为由浏览器 / 真机验证）。
    const mainEl = document.querySelector('main')
    expect(mainEl?.classList.contains('relative')).toBe(true)
  })
})

describe('Layout 侧栏结构（冻结顶/底，仅中间导航可滚）', () => {
  it('aside 为 flex 纵列且裁剪溢出（自身不滚动）', () => {
    renderLayout()
    const aside = document.querySelector('aside')
    expect(aside).not.toBeNull()
    expect(aside?.classList.contains('flex')).toBe(true)
    expect(aside?.classList.contains('flex-col')).toBe(true)
    expect(aside?.classList.contains('overflow-hidden')).toBe(true)
    // 整条侧栏不再自身滚动（回归「整列都滚」）
    expect(aside?.classList.contains('overflow-y-auto')).toBe(false)
  })

  it('中间导航 nav 为唯一可滚区（flex-1 overflow-y-auto）', () => {
    renderLayout()
    const nav = document.querySelector('aside nav')
    expect(nav).not.toBeNull()
    expect(nav?.classList.contains('flex-1')).toBe(true)
    expect(nav?.classList.contains('overflow-y-auto')).toBe(true)
  })

  it('顶部品牌区与底部操作区冻结（shrink-0，不随导航滚动）', () => {
    renderLayout()
    // 品牌区容器（button 父级）冻结
    const brandBox = screen.getByText('Beacon 管理台').closest('button')?.parentElement
    expect(brandBox).not.toBeNull()
    expect(brandBox?.classList.contains('shrink-0')).toBe(true)
    // 底部「开源协议 + 折叠按钮」容器冻结：开源协议链接所在的最近冻结块即底部容器（FR-121 操作人已移走）
    const footer = screen.getByText('开源协议').closest('div.shrink-0.border-t')
    expect(footer).not.toBeNull()
    expect((footer as HTMLElement).classList.contains('shrink-0')).toBe(true)
    expect((footer as HTMLElement).classList.contains('border-t')).toBe(true)
  })

  it('侧栏导航与主内容滚动区隐藏滚动条（scrollbar-hide，保留可滚）', () => {
    renderLayout()
    const nav = document.querySelector('aside nav')
    const mainEl = document.querySelector('main')
    // 仍可滚（overflow-y-auto）但隐藏视觉滚动条（scrollbar-hide）
    expect(nav?.classList.contains('scrollbar-hide')).toBe(true)
    expect(nav?.classList.contains('overflow-y-auto')).toBe(true)
    expect(mainEl?.classList.contains('scrollbar-hide')).toBe(true)
    expect(mainEl?.classList.contains('overflow-y-auto')).toBe(true)
  })
})

describe('Layout 品牌区可点跳可观测看板', () => {
  it('品牌区为可点 button 且保留连接状态小灯（FR-78）', () => {
    renderLayout()
    const brand = screen.getByRole('button', { name: '前往可观测看板' })
    // 连接状态小灯仍在品牌块内：小圆点为带 connection.* 无障碍标签的 rounded-full span
    const dot = brand.querySelector('span.rounded-full[aria-label^="控制面"], span.rounded-full[aria-label^="正在连接"]')
    expect(dot).not.toBeNull()
  })

  it('单击品牌区跳转 /dashboard（延迟判定后，FR-121）', async () => {
    renderLayout('/servers')
    await userEvent.click(screen.getByRole('button', { name: '前往可观测看板' }))
    // 单击延迟 ~200ms 执行跳转（留出双击取消窗口）
    await waitFor(() => expect(navigateSpy).toHaveBeenCalledWith('/dashboard'))
  })

  it('双击品牌区切换侧栏折叠/展开且不触发跳转（FR-121）', async () => {
    setSidebarCollapsed(false)
    renderLayout('/servers')
    expect(document.querySelector('aside')?.classList.contains('w-56')).toBe(true)
    await userEvent.dblClick(screen.getByRole('button', { name: '前往可观测看板' }))
    // 双击折叠 → w-14
    expect(document.querySelector('aside')?.classList.contains('w-14')).toBe(true)
    // 双击取消单击跳转：navigate 未被调用到 /dashboard
    expect(navigateSpy).not.toHaveBeenCalledWith('/dashboard')
  })
})

describe('Layout 搜索入口已从侧栏移除（FR-83）', () => {
  it('侧栏内不再渲染搜索触发块', () => {
    renderLayout()
    const aside = document.querySelector('aside')
    expect(aside).not.toBeNull()
    // 侧栏内不应再出现「搜索…」触发块（已移至右上角页眉）
    const triggers = screen.getAllByText('搜索…')
    for (const el of triggers) {
      expect(aside?.contains(el)).toBe(false)
    }
  })
})

describe('Layout 侧栏导航分组常驻（FR-93 方案 A）', () => {
  it('渲染 5 组分区标题', () => {
    renderLayout()
    for (const label of ['概览', '配置管理', '集群', '可观测', '系统']) {
      expect(screen.getByText(label)).toBeInTheDocument()
    }
  })

  it('叶子常驻显示（不折叠）：未命中路由的组其叶子也直接可见', () => {
    // 当前在 /servers，概览组未命中，但其叶子「可观测看板」仍常驻可见（无折叠）
    renderLayout('/servers')
    expect(screen.getByText('可观测看板')).toBeInTheDocument()
    // 「服务器」此时出现两处：侧栏导航叶子 + 第二层页眉的当前页标题（FR-105），故用 getAllByText 断言至少一处
    expect(screen.getAllByText('服务器').length).toBeGreaterThan(0)
    // 不再使用 details/summary 折叠容器
    expect(document.querySelector('aside details')).toBeNull()
  })

  it('每个叶子项前带 lucide 图标（size-4 svg）', () => {
    renderLayout('/servers')
    const link = screen.getByRole('link', { name: /服务器/ })
    const icon = link.querySelector('svg')
    expect(icon).not.toBeNull()
    expect(icon?.classList.contains('size-4')).toBe(true)
  })

  it('active 项精确单项高亮（end 精确匹配，不被前缀误高亮）', () => {
    // 在 /system 下，仅「控制面健康」(/system) 高亮，/system/version 不应被前缀误命中。
    // 用 classList 精确 token 判定：active 含独立 'bg-sidebar-accent'，
    // 非 active 仅含 'hover:bg-sidebar-accent/50'（不含独立 token）。
    renderLayout('/system')
    const sysHealth = screen.getByRole('link', { name: /控制面健康/ })
    const versionLink = screen.getByRole('link', { name: /版本与更新/ })
    expect(sysHealth.classList.contains('bg-sidebar-accent')).toBe(true)
    expect(versionLink.classList.contains('bg-sidebar-accent')).toBe(false)
    expect(versionLink.classList.contains('font-medium')).toBe(false)
  })
})

describe('Layout 连接状态指示（FR-78）', () => {
  it('连通时不弹断开横幅', async () => {
    vi.mocked(systemStatus).mockResolvedValue(STATUS)
    renderLayout()
    // 等心跳查询成功后，确认无断开横幅
    await screen.findByText('控制面状态')
    await waitFor(() =>
      expect(screen.queryByText('控制面连接中断，正在重连…')).not.toBeInTheDocument(),
    )
  })

  it('心跳查询失败时弹「控制面连接中断，正在重连…」断开横幅', async () => {
    vi.mocked(systemStatus).mockRejectedValue(new Error('网络断开'))
    renderLayout()
    expect(await screen.findByText('控制面连接中断，正在重连…')).toBeInTheDocument()
  })
})

describe('Layout 侧栏可折叠图标条（改进 1）', () => {
  it('默认折叠：侧栏 w-14、品牌区 w-14、隐藏品牌文案与版本徽章', () => {
    // 折叠态渲染（覆盖 beforeEach 的展开预置）
    setSidebarCollapsed(true)
    renderLayout()
    const aside = document.querySelector('aside')
    expect(aside?.classList.contains('w-14')).toBe(true)
    expect(aside?.classList.contains('w-56')).toBe(false)
    // 品牌区容器窄化为 w-14（FR-121：w-14 在 button 父级）、品牌文案不显
    const brandBtn = screen.getByRole('button', { name: '前往可观测看板' })
    expect(brandBtn.parentElement?.classList.contains('w-14')).toBe(true)
    expect(screen.queryByText('Beacon 管理台')).not.toBeInTheDocument()
    // 操作人信息已移至右上角账户菜单（下拉默认收起，不在文档中）
    expect(screen.queryByText('当前操作人')).not.toBeInTheDocument()
  })

  it('折叠态导航叶子仅图标（无文案）且带 title tooltip', () => {
    setSidebarCollapsed(true)
    renderLayout('/servers')
    // 折叠态叶子不渲染文案：导航链接里不再有「服务器」文本节点（仅第二层页眉标题保留该文本）
    const link = screen.getByRole('link', { name: /服务器/ })
    expect(link.getAttribute('title')).toBe('服务器')
    expect(link.querySelector('svg')?.classList.contains('size-4')).toBe(true)
  })

  it('折叠 / 展开切换按钮点击后切换侧栏宽度（持久化）', async () => {
    setSidebarCollapsed(true)
    renderLayout()
    // 初始折叠
    expect(document.querySelector('aside')?.classList.contains('w-14')).toBe(true)
    // 点「展开侧栏」按钮 → 变 w-56
    await userEvent.click(screen.getByRole('button', { name: '展开侧栏' }))
    expect(document.querySelector('aside')?.classList.contains('w-56')).toBe(true)
    // 持久化到 localStorage
    expect(localStorage.getItem('beacon.ui')).toContain('"sidebarCollapsed":false')
  })

  it('展开态显折叠按钮 + 开源协议链接（FR-121：操作人移右上角）', () => {
    setSidebarCollapsed(false)
    renderLayout()
    expect(screen.getByRole('button', { name: '折叠侧栏' })).toBeInTheDocument()
    expect(screen.getByText('开源协议')).toBeInTheDocument()
    // 操作人块不再在侧栏；改为右上角账户菜单头像
    expect(screen.getByRole('button', { name: '账户菜单' })).toBeInTheDocument()
  })
})

describe('Layout FR-121 页眉与侧栏重构', () => {
  it('展开态版本徽章渲染在品牌区（整宽顶栏内、不在侧栏）', async () => {
    setSidebarCollapsed(false)
    renderLayout()
    const version = await screen.findByText('v0.7.0')
    expect(version.closest('header')).not.toBeNull()
    expect(version.closest('aside')).toBeNull()
  })

  it('折叠态隐藏版本徽章', () => {
    setSidebarCollapsed(true)
    renderLayout()
    expect(screen.queryByText('v0.7.0')).toBeNull()
  })

  it('侧栏底部开源协议链接指向仓库 LICENSE（新标签打开）', () => {
    setSidebarCollapsed(false)
    renderLayout()
    const link = screen.getByRole('link', { name: '开源协议' })
    expect(link.getAttribute('href')).toContain('/LICENSE')
    expect(link.getAttribute('target')).toBe('_blank')
    expect(link.getAttribute('rel')).toContain('noreferrer')
  })

  it('右上角账户菜单头像存在（操作人移位）', async () => {
    renderLayout()
    expect(await screen.findByRole('button', { name: '账户菜单' })).toBeInTheDocument()
  })
})
