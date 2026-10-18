import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import AppShell from '../shell/app-shell'
import DocumentTitle from '../shell/document-title'
import {
  SIDEBAR_GUTTER,
  SIDEBAR_WIDTH_COLLAPSED,
  SIDEBAR_WIDTH_EXPANDED,
} from '../shell/sidebar'
import { useShellStore } from '../store'
import '../i18n'

// 在指定路径下渲染完整 Shell（侧栏 + 页眉 + 路由出口）
function renderAt(path: string) {
  const queryClient = new QueryClient()
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <DocumentTitle />
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

/** 页眉段 2 的观测范围选择器（环境 / 命名空间固定在最左，全站统一渲染） */
function observationScopeFilter() {
  const el = document.querySelector('[data-slot="observation-scope-filter"]')
  expect(el).not.toBeNull()
  return el as HTMLElement
}

describe('全站路由', () => {
  // 页面身份自页眉面包屑移除后由内容页 PageHeader 大标题承担；
  // 此处仍以「路由 → 页面标题」真源（routes.tsx titleKey → document.title）校验路由表与 UX.md §2 对齐，
  // 不绑定各页内部结构，避免逐页改造期间互相牵连。
  it.each(pageCases)('%s 页面身份为「%s」', (path, title) => {
    renderAt(path)
    expect(document.title).toBe(`Beacon - ${title}`)
    // 段 2 不再放面包屑，环境 / 命名空间选择器对所有页面统一渲染
    expect(observationScopeFilter()).toBeInTheDocument()
  })

  it('根路径 / 重定向到 /dashboard', () => {
    renderAt('/')
    expect(document.title).toBe('Beacon - 运维总览')
  })

  it('切换页面时重置内容滚动位置并按路径重挂载入场层', async () => {
    const user = userEvent.setup()
    renderAt('/dashboard')
    const main = screen.getByRole('main')
    const initialTransition = document.querySelector('[data-slot="route-transition"]')
    expect(initialTransition).not.toBeNull()

    main.scrollTop = 240
    await user.click(desktopSidebar().getByRole('link', { name: '配置中心' }))

    expect(main.scrollTop).toBe(0)
    expect(document.querySelector('[data-slot="route-transition"]')).not.toBe(initialTransition)
  })

  it('桌面路由内容填满主区且保留 useFitToWidth 的宽度测量', () => {
    renderAt('/approvals')
    const main = screen.getByRole('main')
    const route = main.querySelector('[data-slot="route-transition"]')
    expect(route).not.toBeNull()
    const content = route?.parentElement
    expect(content).toHaveClass('md:flex', 'md:h-full', 'md:min-h-0', 'md:flex-col')
    expect(route).toHaveClass('md:flex-1', 'md:min-h-0')
    expect(content).not.toHaveClass('overflow-hidden')
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

describe('单层页眉（FR-187）', () => {
  it('页眉只占一行：状态点 + 观测范围 + 右侧工具，指标收进弹层', () => {
    renderAt('/dashboard')
    // 页眉合并为单层：不再有独立的指标条容器
    expect(document.querySelector('[data-slot="metrics-strip"]')).toBeNull()
    const appHeader = document.querySelector('[data-slot="app-header"]')
    expect(appHeader).not.toBeNull()
    // 单层页眉内直接包含控制面状态点（同时是指标弹层触发器）
    const controlPlaneStatus = appHeader?.querySelector('[data-slot="control-plane-status"]')
    expect(controlPlaneStatus).not.toBeNull()
    expect(controlPlaneStatus?.getAttribute('data-state')).toMatch(/^(online|offline|unknown)$/)
    expect(controlPlaneStatus?.getAttribute('aria-label')).toMatch(/控制面在线|控制面异常|控制面状态未知/)
    // 指标已收进弹层：未展开时不常驻占用页眉
    expect(screen.queryByText('Agent 在线')).not.toBeInTheDocument()
    // FR-193～196：搜索 / 语言 / 通知 / 刷新均已启用（不再是 comingSoon 占位）
    expect(screen.getByRole('button', { name: '搜索' })).toBeEnabled()
    expect(screen.getByRole('button', { name: '语言' })).toBeEnabled()
    expect(screen.getByRole('button', { name: '通知' })).toBeEnabled()
    expect(screen.getByRole('button', { name: '刷新' })).toBeEnabled()
    // 页眉观测范围选择器仍在
    expect(screen.getByRole('button', { name: '观测环境' })).toBeInTheDocument()
  })

  it('审批页不再有品牌徽标特判：同样渲染观测范围选择器', () => {
    renderAt('/approvals')
    // 页眉布局全站统一：环境 / 命名空间选择器固定在段 2 最左
    expect(observationScopeFilter()).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '观测环境' })).toBeInTheDocument()
    // 旧的「全局审批」徽标已移除（页面内部筛选逻辑不受影响）
    expect(document.querySelector('[data-slot="global-approval-header"]')).toBeNull()
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
    expect(mobile).toHaveClass('rounded-2xl', 'shadow-pop', 'backdrop-blur-sm')
    expect(mobile).toHaveStyle({
      left: `${String(SIDEBAR_GUTTER)}px`,
      top: `${String(SIDEBAR_GUTTER)}px`,
      bottom: `${String(SIDEBAR_GUTTER)}px`,
    })
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
    expect(aside).toHaveStyle({
      width: `${String(SIDEBAR_WIDTH_EXPANDED)}px`,
      left: `${String(SIDEBAR_GUTTER)}px`,
      top: `${String(SIDEBAR_GUTTER)}px`,
      bottom: `${String(SIDEBAR_GUTTER)}px`,
    })
    // 悬浮圆角卡片形态（四周留白 + 边框 + 阴影），不复刻连体外壳
    expect(aside).toHaveClass('rounded-2xl', 'border-border/60', 'shadow-pop', 'backdrop-blur-sm')
    expect(aside).toHaveClass('duration-[400ms]', 'ease-[cubic-bezier(0.22,1,0.36,1)]', 'delay-0')
    expect(aside).not.toHaveClass('motion-reduce:transition-none')
    const shellLayout = document.querySelector('[style*="--shell-sidebar-width"]')
    expect(shellLayout).toHaveStyle({
      '--shell-sidebar-width': `${String(SIDEBAR_WIDTH_EXPANDED + SIDEBAR_GUTTER * 2)}px`,
    })
    expect(shellLayout).toHaveClass('duration-[400ms]', 'ease-[cubic-bezier(0.22,1,0.36,1)]')
    // 折叠按钮已移入侧栏内部（品牌行右端），不再是贴合卡缘的浮动按钮
    const collapseBtn = screen.getByRole('button', { name: '收起导航' })
    expect(aside).toContainElement(collapseBtn)
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

  it('页眉与内容区同底透明，侧栏为独立悬浮圆角卡片', () => {
    renderAt('/dashboard')
    const headerShell = document.querySelector('[data-slot="app-header"]')
    const header = document.querySelector('[data-slot="app-header"] header')
    const aside = document.querySelector('[data-slot="desktop-sidebar"]')
    const contentSurface = document.querySelector('[data-slot="route-transition"]')?.parentElement

    // 页眉外层不铺实色底，段 2 透明（FR-191），侧栏保持自带边框/阴影的悬浮卡片
    expect(headerShell).not.toHaveClass('bg-surface-2')
    expect(aside).toHaveClass('rounded-2xl', 'shadow-pop', 'backdrop-blur-sm', 'bg-sidebar/95')
    // 内容区直接铺在应用背景上，不再套独立圆角卡面
    expect(contentSurface).not.toHaveClass('bg-card')
    expect(header).not.toBeNull()
    expect(header?.className ?? '').not.toMatch(/border-b/)
    expect(header?.className ?? '').not.toMatch(/backdrop-blur/)
  })

  it('观测环境为无底 Dropdown 触发器（FR-192）', () => {
    renderAt('/dashboard')
    const trigger = screen.getByRole('button', { name: '观测环境' })
    expect(trigger).toBeInTheDocument()
    expect(trigger.closest('[data-slot="observation-scope-filter"]')).not.toBeNull()
  })

  it('点击收起后进入图标轨：仍可按 aria-label 导航，宽度为折叠值', async () => {
    const user = userEvent.setup()
    renderAt('/dashboard')
    await user.click(screen.getByRole('button', { name: '收起导航' }))
    const aside = document.querySelector('[data-slot="desktop-sidebar"]')
    expect(aside).toHaveAttribute('data-collapsed', 'true')
    expect(aside).toHaveStyle({ width: `${String(SIDEBAR_WIDTH_COLLAPSED)}px` })
    expect(aside).toHaveClass('delay-[50ms]')
    const shellLayout = document.querySelector('[style*="--shell-sidebar-width"]')
    expect(shellLayout).toHaveStyle({
      '--shell-sidebar-width': `${String(SIDEBAR_WIDTH_COLLAPSED + SIDEBAR_GUTTER * 2)}px`,
    })
    expect(shellLayout).toHaveClass('delay-[50ms]')
    // 收起态折叠按钮移到 logo 下方，仍在侧栏内部
    const expandBtn = screen.getByRole('button', { name: '展开导航' })
    expect(aside).toContainElement(expandBtn)
    // 折叠态：侧栏内不再承载控制面状态点（已移回页眉），此处只验证折叠按钮位置
    expect(aside?.querySelector('[data-slot="control-plane-status"]')).toBeNull()
    // 折叠后分组标题隐藏，但链接仍以 aria-label 可达
    const nav = desktopSidebar()
    expect(nav.getByRole('link', { name: '配置中心' })).toBeInTheDocument()
    expect(nav.getByRole('link', { name: '配置中心' })).toHaveAttribute('title', '配置中心')
    // 分组文案不再可见（桌面折叠轨）
    expect(nav.queryByText('交付')).not.toBeInTheDocument()
    // 可再展开
    await user.click(screen.getByRole('button', { name: '展开导航' }))
    expect(aside).toHaveAttribute('data-collapsed', 'false')
    expect(aside).toHaveStyle({ width: `${String(SIDEBAR_WIDTH_EXPANDED)}px` })
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
