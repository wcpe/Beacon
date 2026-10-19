import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { act, render, screen, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

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
  vi.useRealTimers()
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

    // 数值由 JumpValue 渲染（更新时会重建元素播放跳动动画），
    // 故按容器取 textContent，而不是依赖稳定的文本节点匹配。
    await waitFor(() => {
      expect(container.querySelector('[data-slot="resource-cpu"]')?.textContent).toContain('92%')
    })
    expect(container.querySelector('[data-slot="resource-cpu"]')).toHaveAttribute('data-level', 'warning')
    expect(container.querySelector('[data-slot="resource-memory"]')).toHaveAttribute('data-level', 'warning')
    expect(container.querySelector('[data-slot="resource-memory"]')?.textContent).toContain('110/128 MB')
    // 警告色仍施加在数值元素上
    const cpuValue = container.querySelector('[data-slot="resource-cpu"] .animate-value-jump')
    expect(cpuValue).not.toBeNull()
    expect(cpuValue).toHaveClass('text-warn')
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
    const { queryClient, container } = renderResources()
    // 数值由 JumpValue 渲染，元素会随更新重建，按容器取文本更稳
    await waitFor(() => {
      expect(container.querySelector('[data-slot="resource-cpu"]')?.textContent).toContain('42%')
    })

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

  it('后台 8 秒轮询请求期间同样展示忙碌标记', async () => {
    vi.useFakeTimers()
    let finishPoll: (() => void) | null = null
    // 不设 staleTime：设为 Infinity 时 React Query 认为缓存永久新鲜，
    // 轮询到点也不会真正发请求，isFetching 恒为 false，测不到忙碌态。
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    queryClient.setQueryData(['shell', 'control-plane-resources'], status)
    server.use(
      http.get('/admin/v1/system/status', async () => {
        await new Promise<void>((resolve) => {
          finishPoll = resolve
        })
        return HttpResponse.json(status)
      }),
    )
    const rendered = render(
      <MemoryRouter>
        <QueryClientProvider client={queryClient}>
          <ControlPlaneResources />
        </QueryClientProvider>
      </MemoryRouter>,
    )
    const resources = rendered.container.querySelector('[data-slot="control-plane-resources"]')
    // 挂载即触发首次请求，取数期间本就应处于忙碌态（数据在加载）。
    // 这里等到首轮请求完成、忙碌态收起，再推进到轮询点验证第二轮。
    await act(async () => {
      await Promise.resolve()
    })

    await act(async () => {
      await vi.advanceTimersByTimeAsync(CONTROL_PLANE_RESOURCES_REFETCH_MS)
    })

    // 轮询取数期间数值会更新；若不标记忙碌，用户看到的是数字被硬替换，
    // 无法判断它刚更新过。故轮询与主动刷新都应有忙碌反馈。
    const fetchingCount = queryClient.isFetching({ queryKey: ['shell', 'control-plane-resources'] })
    expect(fetchingCount, 'staleTime 已移除，轮询到点应真正发出请求').toBe(1)
    // 请求在途时组件应已重渲染出忙碌态；等待一个微任务让 React 提交更新
    await act(async () => {
      await Promise.resolve()
    })
    expect(resources).toHaveAttribute('aria-busy', 'true')
    expect(screen.getByText('42%')).toBeInTheDocument()
    expect(screen.getByText('64/128 MB')).toBeInTheDocument()

    await act(async () => {
      finishPoll?.()
      await Promise.resolve()
    })
    rendered.unmount()
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
