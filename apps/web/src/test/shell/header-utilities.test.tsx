// 页眉工具：搜索可点、刷新可点、语言/通知触发器存在（FR-193～196）
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { MemoryRouter } from 'react-router-dom'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import Header from '../../shell/header'
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
