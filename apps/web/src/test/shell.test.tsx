import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import AppShell from '../shell/app-shell'
import DocumentTitle from '../shell/document-title'
import { SIDEBAR_WIDTH_COLLAPSED, SIDEBAR_WIDTH_EXPANDED } from '../shell/sidebar'
import { useShellStore } from '../store'
import '../i18n'

// 在指定路径下渲染完整 Shell（侧栏 + 页眉 + 路由出口）
function renderAt(path: string) {
  const queryClient = new QueryClient()
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <AppShell />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

/** 桌面侧栏作用域（避免与小屏抽屉内重复 navigation 冲突） */
function desktopSidebar() {
  const el = document.querySelector('[data-slot="desktop-sidebar"]')
  expect(el).not.toBeNull()
  return within(el as HTMLElement)
}

// UX.md §2 的路由与页面标题（真源对照表）
const pageCases: [string, string][] = [
  ['/dashboard', '运维总览'],
  ['/servers', '服务器'],
  ['/identity-conflicts', '身份冲突'],
  ['/zones', '区服分配'],
  ['/topology', '拓扑'],
  ['/service-analysis', '服务分析'],
  ['/commands', '命令观测'],
  ['/audits', '审计'],
  ['/alert-events', '告警事件'],
  ['/assets', '文件资产'],
  ['/configs', '配置中心'],
  ['/changes', '变更单'],
  ['/changes/history', '交付历史'],
  ['/settings', '运维设置'],
  ['/system', '控制面健康'],
  ['/system/version', '版本与更新'],
  ['/api-keys', '密钥'],
  ['/namespaces', '命名空间'],
  ['/envs', '环境'],
]

describe('全站路由', () => {
  it.each(pageCases)('%s 渲染页面标题「%s」', (path, title) => {
    renderAt(path)
    expect(screen.getByRole('heading', { name: title })).toBeInTheDocument()
  })

  it('根路径 / 重定向到 /dashboard', () => {
    renderAt('/')
    expect(screen.getByRole('heading', { name: '运维总览' })).toBeInTheDocument()
  })
})

describe('侧栏导航', () => {
  it('含顶层运维总览与四个大域分组标题', () => {
    renderAt('/dashboard')
    const nav = desktopSidebar()
    expect(nav.getByRole('link', { name: '运维总览' })).toBeInTheDocument()
    for (const group of ['集群', '可观测', '交付', '系统']) {
      expect(nav.getByText(group)).toBeInTheDocument()
    }
  })

  it('交付大域挂文件资产 / 配置中心 / 变更单 / 交付历史四页', () => {
    renderAt('/dashboard')
    const nav = desktopSidebar()
    const deliveryPages: [string, string][] = [
      ['文件资产', '/assets'],
      ['配置中心', '/configs'],
      ['变更单', '/changes'],
      ['交付历史', '/changes/history'],
    ]
    for (const [label, path] of deliveryPages) {
      expect(nav.getByRole('link', { name: label })).toHaveAttribute('href', path)
    }
  })

  it('当前路由对应的导航项高亮（aria-current=page）', () => {
    renderAt('/configs')
    const nav = desktopSidebar()
    expect(nav.getByRole('link', { name: '配置中心' })).toHaveAttribute('aria-current', 'page')
    expect(nav.getByRole('link', { name: '变更单' })).not.toHaveAttribute('aria-current')
  })
})

describe('双段页眉（FR-187）', () => {
  it('渲染段 1 指标槽与段 2 工具占位', () => {
    renderAt('/dashboard')
    expect(document.querySelector('[data-slot="metrics-strip"]')).not.toBeNull()
    expect(document.querySelector('[data-slot="app-header"]')).not.toBeNull()
    // 段 1 含指标标签（真数据异步，标签先可见）
    expect(screen.getByText('Agent 在线')).toBeInTheDocument()
    expect(screen.getByText('待确认')).toBeInTheDocument()
    // FR-193～196：搜索 / 语言 / 通知 / 刷新均已启用（不再是 comingSoon 占位）
    expect(screen.getByRole('button', { name: '搜索' })).toBeEnabled()
    expect(screen.getByRole('button', { name: '语言' })).toBeEnabled()
    expect(screen.getByRole('button', { name: '通知' })).toBeEnabled()
    expect(screen.getByRole('button', { name: '刷新' })).toBeEnabled()
    // 环境过滤仍在
    expect(screen.getByLabelText('环境过滤器')).toBeInTheDocument()
  })
})

describe('小屏抽屉侧栏（FR-189）', () => {
  beforeEach(() => {
    useShellStore.setState({ sidebarCollapsed: false, mobileNavOpen: false })
  })

  it('默认关闭：移动侧栏 data-open=false，可点汉堡打开', async () => {
    const user = userEvent.setup()
    renderAt('/dashboard')
    const mobile = document.querySelector('[data-slot="mobile-sidebar"]')
    expect(mobile).not.toBeNull()
    expect(mobile).toHaveAttribute('data-open', 'false')
    await user.click(screen.getByRole('button', { name: '打开导航' }))
    expect(mobile).toHaveAttribute('data-open', 'true')
    // 抽屉内可导航
    expect(within(mobile as HTMLElement).getByRole('link', { name: '配置中心' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '关闭导航' }))
    expect(mobile).toHaveAttribute('data-open', 'false')
  })
})

describe('侧栏图标轨折叠（FR-186）', () => {
  beforeEach(() => {
    useShellStore.setState({ sidebarCollapsed: false, mobileNavOpen: false })
  })

  it('默认展开：品牌仅 Beacon、无「管理台」、底栏含版本与开源许可', () => {
    renderAt('/dashboard')
    const aside = document.querySelector('[data-slot="desktop-sidebar"]')
    expect(aside).not.toBeNull()
    expect(aside).toHaveAttribute('data-collapsed', 'false')
    expect(aside).toHaveStyle({ width: `${SIDEBAR_WIDTH_EXPANDED}px` })
    // 品牌无副标题
    expect(aside?.textContent ?? '').not.toContain('管理台')
    // 身份只在页眉
    expect(aside?.textContent ?? '').not.toContain('超级管理员')
    expect(screen.queryByText('超级管理员')).not.toBeInTheDocument()
    // 底栏开源协议：站内 /license（FR-190，用语对齐旧版）
    const footer = aside?.querySelector('[data-slot="sidebar-footer"]')
    expect(footer).not.toBeNull()
    expect(within(footer as HTMLElement).getByRole('link', { name: '开源协议' })).toHaveAttribute(
      'href',
      '/license',
    )
  })

  it('段 2 页眉无横线隔断（FR-191）', () => {
    renderAt('/dashboard')
    const header = document.querySelector('[data-slot="app-header"] header')
    expect(header).not.toBeNull()
    expect(header?.className ?? '').not.toMatch(/border-b/)
    expect(header?.className ?? '').not.toMatch(/backdrop-blur/)
  })

  it('环境过滤器为无底 Dropdown 触发器（FR-192）', () => {
    renderAt('/dashboard')
    const trigger = screen.getByRole('button', { name: '环境过滤器' })
    expect(trigger).toBeInTheDocument()
    expect(trigger.getAttribute('data-slot')).toBe('env-filter-trigger')
  })

  it('点击收起后进入图标轨：仍可按 aria-label 导航，宽度为折叠值', async () => {
    const user = userEvent.setup()
    renderAt('/dashboard')
    await user.click(screen.getByRole('button', { name: '收起导航' }))
    const aside = document.querySelector('[data-slot="desktop-sidebar"]')
    expect(aside).toHaveAttribute('data-collapsed', 'true')
    expect(aside).toHaveStyle({ width: `${SIDEBAR_WIDTH_COLLAPSED}px` })
    // 折叠后分组标题隐藏，但链接仍以 aria-label 可达
    const nav = desktopSidebar()
    expect(nav.getByRole('link', { name: '配置中心' })).toBeInTheDocument()
    expect(nav.getByRole('link', { name: '配置中心' })).toHaveAttribute('title', '配置中心')
    // 分组文案不再可见（桌面折叠轨）
    expect(nav.queryByText('交付')).not.toBeInTheDocument()
    // 可再展开
    await user.click(screen.getByRole('button', { name: '展开导航' }))
    expect(aside).toHaveAttribute('data-collapsed', 'false')
    expect(aside).toHaveStyle({ width: `${SIDEBAR_WIDTH_EXPANDED}px` })
  })
})

describe('浏览器标签标题', () => {
  it.each([
    ['/dashboard', 'Beacon - 运维总览'],
    ['/servers', 'Beacon - 服务器'],
    ['/changes', 'Beacon - 变更单'],
    ['/settings', 'Beacon - 运维设置'],
  ] as const)('%s → document.title = %s', (path, expected) => {
    const queryClient = new QueryClient()
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={[path]}>
          <DocumentTitle />
          <AppShell />
        </MemoryRouter>
      </QueryClientProvider>,
    )
    expect(document.title).toBe(expected)
  })

  it('登录路径标题为 Beacon - 登录', () => {
    render(
      <MemoryRouter initialEntries={['/login']}>
        <DocumentTitle />
      </MemoryRouter>,
    )
    expect(document.title).toBe('Beacon - 登录')
  })
})
