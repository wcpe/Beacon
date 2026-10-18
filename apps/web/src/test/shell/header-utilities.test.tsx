// 页眉工具：搜索可点、刷新可点、语言/通知触发器存在（FR-193～196）
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { MemoryRouter } from 'react-router-dom'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import Header from '../../shell/header'
import OperatorMenu from '../../shell/operator-menu'
import { setLocale } from '../../state/locale'
import '../../i18n'

const server = setupServer(
  http.get('/admin/v1/system/status', () =>
    HttpResponse.json({
      version: '0.31.0',
      startedAt: '2026-07-22T00:00:00Z',
      uptimeSeconds: 1,
      db: { connected: true },
      onlineInstances: 0,
      samplerEnabled: true,
      runtime: { goroutines: 1, heapAlloc: 1, heapSys: 1 },
      cpuAvailable: false,
      cpuPercent: 0,
    }),
  ),
  http.get('/admin/v2/servers', () => HttpResponse.json({ items: [], total: 0 })),
  http.get('/admin/v2/agent-identities', () => HttpResponse.json({ items: [], total: 0 })),
  http.get('/admin/v1/alert-events', () => HttpResponse.json({ items: [], total: 0 })),
  http.get('/admin/v2/change-orders', () => HttpResponse.json({ items: [], total: 0 })),
  http.get('/admin/v2/envs', () => HttpResponse.json({ items: [], total: 0 })),
)

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
afterEach(() => {
  server.resetHandlers()
  setLocale('zh-CN')
})
afterAll(() => {
  server.close()
})

function renderHeader() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <Header />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('页眉工具 FR-193～196', () => {
  beforeEach(() => {
    setLocale('zh-CN')
  })

  it('单层页眉显示独立的控制面资源指示器', async () => {
    renderHeader()
    const resourcesLink = await screen.findByRole('link', {
      name: '控制面 CPU 与堆内存，点击查看健康详情',
    })
    expect(resourcesLink).toHaveAttribute('href', '/system')
  })

  it('窄屏保留关键入口并收敛次要页眉项', () => {
    renderHeader()
    const header = document.querySelector('[data-slot="app-header"] header')
    const status = document.querySelector('[data-slot="control-plane-status"]')
    const statusLabel = document.querySelector('[data-slot="control-plane-status-label"]')
    const namespace = document.querySelector('[data-slot="observation-scope-namespace"]')
    const envTrigger = screen.getByRole('button', { name: '观测环境' })

    expect(header).toHaveClass('h-12', 'px-3', 'sm:px-4')
    expect(envTrigger).toHaveClass('max-w-[7rem]')
    expect(envTrigger.querySelector('span.truncate')).toHaveTextContent('全部环境')
    expect(document.querySelector('[data-slot="header-search"]')).toHaveAttribute('aria-label', '搜索')
    expect(document.querySelector('[data-slot="notifications-trigger"]')).toHaveAttribute('aria-label', '通知')
    expect(status).toHaveAttribute('aria-label')
    expect(statusLabel).toHaveClass('hidden', 'xl:inline')
    expect(namespace).toHaveClass('hidden', 'sm:flex')
    // 语言是硬功能入口，sm 起就保留（平板/手机横屏可用）；刷新仅 lg 起，小屏靠下拉整页刷新。
    expect(document.querySelector('[data-slot="header-language-compact"]')).toHaveClass('hidden', 'sm:inline-flex')
    expect(document.querySelector('[data-slot="header-refresh-compact"]')).toHaveClass('hidden', 'lg:inline-flex')
  })

  it('窄屏账户入口保留可访问的头像触发器', () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <OperatorMenu />
        </MemoryRouter>
      </QueryClientProvider>,
    )

    const account = screen.getByRole('button', { name: '账户菜单' })
    expect(account).toBeEnabled()
    expect(account).toHaveClass('w-8', 'px-0', 'sm:w-auto')
    expect(account.querySelector('span.size-7')).not.toBeNull()
    expect(account.querySelector('svg')).toHaveClass('hidden', 'sm:inline')
  })

  it('搜索按钮可点并打开命令面板', async () => {
    const user = userEvent.setup()
    renderHeader()
    await user.click(screen.getByRole('button', { name: '搜索' }))
    expect(await screen.findByRole('listbox', { name: '全局搜索' })).toBeInTheDocument()
    expect(screen.getByLabelText('搜索页面、服务器、审计动作…')).toBeInTheDocument()
  })

  it('刷新按钮可点（不整页 reload）', async () => {
    const user = userEvent.setup()
    renderHeader()
    const btn = await screen.findByRole('button', { name: '刷新' })
    expect(btn).toBeEnabled()
    await user.click(btn)
    // 仍在文档中（未 unload）
    expect(screen.getByRole('button', { name: '刷新' })).toBeInTheDocument()
  })

  it('语言与通知触发器启用', async () => {
    renderHeader()
    expect(await screen.findByRole('button', { name: '语言' })).toBeEnabled()
    expect(screen.getByRole('button', { name: '通知' })).toBeEnabled()
  })
})
