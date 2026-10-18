import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { act, render, screen, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import ControlPlaneResources from '../shell/control-plane-resources'
import { CONTROL_PLANE_RESOURCES_REFETCH_MS } from '../shell/use-control-plane-resources'
import { i18next } from '../i18n'
import { setLocale } from '../state/locale'

const status = {
  version: '1.5.0',
  startedAt: '2026-10-18T00:00:00Z',
  uptimeSeconds: 60,
  db: { connected: true },
  onlineInstances: 3,
  samplerEnabled: true,
  runtime: { goroutines: 10, heapAlloc: 64 * 1024 * 1024, heapSys: 128 * 1024 * 1024 },
  cpuAvailable: true,
  cpuPercent: 42.4,
}

const server = setupServer(
  http.get('/admin/v1/system/status', () => HttpResponse.json(status)),
)

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
afterEach(async () => {
  server.resetHandlers()
  setLocale('zh-CN')
  await i18next.changeLanguage('zh-CN')
})
afterAll(() => {
  server.close()
})
beforeEach(async () => {
  setLocale('zh-CN')
  await i18next.changeLanguage('zh-CN')
})

function renderResources() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const rendered = render(
    <MemoryRouter>
      <QueryClientProvider client={queryClient}>
        <ControlPlaneResources />
      </QueryClientProvider>
    </MemoryRouter>,
  )
  return { queryClient, ...rendered }
}

describe('页眉控制面资源指示器（FR-272）', () => {
  it('每 8 秒轮询一次资源快照', () => {
    expect(CONTROL_PLANE_RESOURCES_REFETCH_MS).toBe(8_000)
  })

  it('展示控制面进程 CPU 与堆内存', async () => {
    renderResources()
    const group = await screen.findByRole('link', { name: '控制面 CPU 42%，堆内存 64/128 MB，点击查看健康详情' })
    expect(group).toHaveAttribute('href', '/system')
    expect(group).toHaveAttribute('title', '查看控制面健康')
    expect(group).toHaveClass(
      'hidden',
      'lg:flex',
      'w-[min(260px,30vw)]',
      'min-w-[200px]',
      'rounded-lg',
      'px-2',
      'bg-surface-2/70',
      'transition-colors',
      'hover:bg-surface-2',
      'cursor-pointer',
      'focus-visible:ring-2',
    )

    expect(await screen.findByText('42%')).toBeInTheDocument()
    expect(screen.getByTitle('42.4%')).toHaveClass('h-1.5', 'w-10', 'bg-ink-3/25')
    expect(screen.getByTitle('42.4%').firstElementChild?.classList.contains('motion-reduce:transition-none')).toBe(true)
    expect(screen.getByText('64/128 MB')).toBeInTheDocument()
  })

  it('CPU 或堆使用率超过 80% 时应用警告色', async () => {
    server.use(
      http.get('/admin/v1/system/status', () =>
        HttpResponse.json({
          ...status,
          cpuPercent: 92.4,
          runtime: { goroutines: 10, heapAlloc: 110 * 1024 * 1024, heapSys: 128 * 1024 * 1024 },
        }),
      ),
    )
    const { container } = renderResources()

    expect(await screen.findByText('92%')).toBeInTheDocument()
    expect(container.querySelector('[data-slot="resource-cpu"]')).toHaveAttribute('data-level', 'warning')
    expect(container.querySelector('[data-slot="resource-memory"]')).toHaveAttribute('data-level', 'warning')
    expect(screen.getByText('92%')).toHaveClass('text-warn')
    expect(screen.getByText('110/128 MB')).toHaveClass('text-warn')
  })

  it('首次加载或接口失败时显示占位符', async () => {
    server.use(
      http.get('/admin/v1/system/status', () => HttpResponse.json({ code: 'unavailable' }, { status: 500 })),
    )
    const { container } = renderResources()

    expect(container.querySelector('[data-slot="resource-cpu"]')).toHaveTextContent('—')
    expect(container.querySelector('[data-slot="resource-memory"]')).toHaveTextContent('—')
    await waitFor(() => expect(container.querySelector('[data-slot="resource-cpu"]')).toHaveTextContent('—'))
  })

  it('轮询失败时保留上次成功的数据', async () => {
    let calls = 0
    let failures = 0
    server.use(
      http.get('/admin/v1/system/status', () => {
        calls += 1
        return HttpResponse.json(status)
      }),
    )
    const { queryClient } = renderResources()
    expect(await screen.findByText('42%')).toBeInTheDocument()

    server.use(
      http.get('/admin/v1/system/status', () => {
        failures += 1
        return HttpResponse.json({ code: 'unavailable' }, { status: 500 })
      }),
    )
    await act(async () => {
      await queryClient.invalidateQueries({ queryKey: ['shell', 'control-plane-resources'] })
    })

    expect(calls).toBeGreaterThan(0)
    expect(failures).toBeGreaterThan(0)
    expect(screen.getByText('42%')).toBeInTheDocument()
    expect(screen.getByText('64/128 MB')).toBeInTheDocument()
  })

  it('英文界面使用对应 i18n 标签', async () => {
    setLocale('en')
    await act(async () => {
      await i18next.changeLanguage('en')
    })
    renderResources()
    const resourcesLink = await screen.findByRole('link', {
      name: 'Control-plane CPU 42%, heap memory 64/128 MB; click to view health details',
    })
    expect(resourcesLink).toHaveAttribute('title', 'View control-plane health')
    expect(await screen.findByText('Memory')).toBeInTheDocument()
  })
})
