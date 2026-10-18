// FR-188：全局运维指标条聚合与降级
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import MetricsStrip from '../shell/metrics-strip'
import { i18next } from '../i18n'
import { setLocale } from '../state/locale'
import Sidebar from '../shell/sidebar'
import { useShellStore } from '../store'
import '../i18n'

function requestUrl(input: RequestInfo | URL): string {
  if (typeof input === 'string') {
    return input
  }
  if (input instanceof URL) {
    return input.href
  }
  return input.url
}

function renderStrip() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <Sidebar />
        <MetricsStrip />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  useShellStore.setState({ sidebarCollapsed: false, mobileNavOpen: false })
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = requestUrl(input)
      if (url.includes('/admin/v1/system/status')) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              version: '0.30.0',
              startedAt: '2026-07-21T00:00:00Z',
              uptimeSeconds: 100,
              db: { connected: true },
              onlineInstances: 3,
              samplerEnabled: true,
              runtime: { goroutines: 10, heapAlloc: 1, heapSys: 2 },
              cpuAvailable: true,
              cpuPercent: 1,
            }),
            { status: 200, headers: { 'Content-Type': 'application/json' } },
          ),
        )
      }
      if (url.includes('/admin/v2/servers')) {
        return Promise.resolve(
          new Response(JSON.stringify({ items: [], total: 0 }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          }),
        )
      }
      if (url.includes('/admin/v2/agent-identities')) {
        return Promise.resolve(
          new Response(JSON.stringify({ items: [{ id: 'x' }], total: 2 }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          }),
        )
      }
      if (url.includes('/admin/v1/alert-events')) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              items: [
                {
                  id: 1,
                  type: 'health-transition',
                  level: 'warning',
                  serverId: 's1',
                  namespace: 'ns',
                  message: 'm',
                  detail: 'd',
                  createdAt: '2026-07-21T00:00:00Z',
                  status: 'open',
                  handledBy: null,
                  handledAt: null,
                  handleNote: null,
                },
                {
                  id: 2,
                  type: 'health-transition',
                  level: 'info',
                  serverId: 's2',
                  namespace: 'ns',
                  message: 'm2',
                  detail: 'd2',
                  createdAt: '2026-07-21T00:00:00Z',
                  status: 'resolved',
                  handledBy: 'a',
                  handledAt: '2026-07-21T01:00:00Z',
                  handleNote: null,
                },
              ],
              total: 2,
            }),
            { status: 200, headers: { 'Content-Type': 'application/json' } },
          ),
        )
      }
      if (url.includes('/admin/v2/change-orders')) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              items: [
                { id: 1, status: 'rolling', title: 'a' },
                { id: 2, status: 'completed', title: 'b' },
                { id: 3, status: 'paused', title: 'c' },
              ],
              total: 3,
            }),
            { status: 200, headers: { 'Content-Type': 'application/json' } },
          ),
        )
      }
      return Promise.resolve(
        new Response(JSON.stringify({ code: 'not_found', message: url }), { status: 404 }),
      )
    }),
  )
})

afterEach(async () => {
  vi.unstubAllGlobals()
  setLocale('zh-CN')
  await i18next.changeLanguage('zh-CN')
})

function metricValue(label: string): string {
  const labelEl = screen.getByText(label)
  const link = labelEl.closest('a')
  expect(link).not.toBeNull()
  const valueEl = link?.querySelector('.tabular-nums')
  expect(valueEl).not.toBeNull()
  return valueEl?.textContent ?? ''
}

describe('全局运维指标（FR-188）', () => {
  it('状态点说明在线状态与非零计数，且双语同步到悬停提示', async () => {
    const { container } = renderStrip()
    const status = container.querySelector('[data-slot="control-plane-status"]')
    const zhDescription = '控制面在线 · 待确认: 2 · 未处理告警: 1'
    await waitFor(() => {
      // 控制面状态点现在是页眉内的弹层触发器（页眉已合并为单层）
      expect(status).toHaveAttribute('data-state', 'online')
      expect(status).toHaveAttribute('aria-label', zhDescription)
      expect(status).toHaveAttribute('title', zhDescription)
    })
    expect(status?.querySelectorAll('[aria-hidden="true"]')).toHaveLength(2)

    setLocale('en')
    await act(async () => {
      await i18next.changeLanguage('en')
    })
    const enDescription = 'Control plane online · Pending: 2 · Open alerts: 1'
    await waitFor(() => {
      expect(status).toHaveAttribute('aria-label', enDescription)
      expect(status).toHaveAttribute('title', enDescription)
    })

    setLocale('zh-CN')
    await act(async () => {
      await i18next.changeLanguage('zh-CN')
    })
    // 指标收进弹层：未打开前不应直接出现在页面上
    expect(screen.queryByText('Agent 在线')).not.toBeInTheDocument()

    await userEvent.click(status as HTMLElement)
    await waitFor(() => {
      expect(metricValue('Agent 在线')).toBe('3')
    })
    expect(metricValue('待确认')).toBe('2')
    expect(metricValue('未处理告警')).toBe('1')
    // rolling + paused = 2
    expect(metricValue('进行中变更单')).toBe('2')
  })

  it('加载期间页眉控制面状态使用中性色未知态', () => {
    const { container } = renderStrip()
    const status = container.querySelector('[data-slot="control-plane-status"]')
    expect(status).toHaveAttribute('data-state', 'unknown')
    expect(status).toHaveAttribute('aria-label', '控制面状态未知')
  })

  it('status 失败时侧栏控制面显示异常且其它项可仍为 — 或数', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = requestUrl(input)
        if (url.includes('/admin/v1/system/status')) {
          return Promise.resolve(
            new Response(JSON.stringify({ code: 'err', message: 'down' }), { status: 500 }),
          )
        }
        if (url.includes('/admin/v2/agent-identities')) {
          return Promise.resolve(
            new Response(JSON.stringify({ items: [], total: 0 }), {
              status: 200,
              headers: { 'Content-Type': 'application/json' },
            }),
          )
        }
        if (url.includes('/admin/v2/servers')) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                items: [
                  { id: 1, online: true },
                  { id: 2, online: false },
                ],
                total: 2,
              }),
              { status: 200, headers: { 'Content-Type': 'application/json' } },
            ),
          )
        }
        if (url.includes('/admin/v1/alert-events') || url.includes('/admin/v2/change-orders')) {
          return Promise.resolve(
            new Response(JSON.stringify({ code: 'err', message: 'x' }), { status: 500 }),
          )
        }
        return Promise.resolve(new Response('{}', { status: 404 }))
      }),
    )
    const { container } = renderStrip()
    await waitFor(() => {
      const status = container.querySelector('[data-slot="control-plane-status"]')
      expect(status).toHaveAttribute('data-state', 'offline')
      expect(status).toHaveAttribute('aria-label', '控制面异常')
    })
    // 指标在弹层内：打开后服务器在线数回落为 1
    await userEvent.click(screen.getByRole('button', { name: '控制面异常' }))
    await waitFor(() => {
      expect(screen.getByText('1')).toBeInTheDocument()
    })
    // 告警/变更失败 → —
    expect(screen.getAllByText('—').length).toBeGreaterThanOrEqual(1)
  })
})
