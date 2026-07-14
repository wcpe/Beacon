import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'

import AppShell from '../shell/app-shell'
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

// UX.md §2 的 18 条路由与页面标题（真源对照表）
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
    const nav = within(screen.getByRole('navigation'))
    expect(nav.getByRole('link', { name: '运维总览' })).toBeInTheDocument()
    for (const group of ['集群', '可观测', '交付', '系统']) {
      expect(nav.getByText(group)).toBeInTheDocument()
    }
  })

  it('交付大域挂文件资产 / 配置中心 / 变更单 / 交付历史四页', () => {
    renderAt('/dashboard')
    const nav = within(screen.getByRole('navigation'))
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
    const nav = within(screen.getByRole('navigation'))
    expect(nav.getByRole('link', { name: '配置中心' })).toHaveAttribute('aria-current', 'page')
    expect(nav.getByRole('link', { name: '变更单' })).not.toHaveAttribute('aria-current')
  })
})
