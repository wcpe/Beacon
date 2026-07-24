// FR-188：全局运维指标条聚合与降级
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import MetricsStrip from '../shell/metrics-strip'
import '../i18n'

function renderStrip() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <MetricsStrip />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo) => {
      const url = String(input)
      if (url.includes('/admin/v1/system/status')) {
        return new Response(
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
        )
      }
      if (url.includes('/admin/v2/servers')) {
        return new Response(JSON.stringify({ items: [], total: 0 }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      }
      if (url.includes('/admin/v2/agent-identities')) {
        return new Response(JSON.stringify({ items: [{ id: 'x' }], total: 2 }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      }
      if (url.includes('/admin/v1/alert-events')) {
        return new Response(
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
        )
      }
      if (url.includes('/admin/v2/change-orders')) {
        return new Response(
          JSON.stringify({
            items: [
              { id: 1, status: 'rolling', title: 'a' },
              { id: 2, status: 'completed', title: 'b' },
              { id: 3, status: 'paused', title: 'c' },
            ],
            total: 3,
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        )
      }
      return new Response(JSON.stringify({ code: 'not_found', message: url }), { status: 404 })
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

function metricValue(label: string): string {
  const labelEl = screen.getByText(label)
  const link = labelEl.closest('a')
  expect(link).not.toBeNull()
  const valueEl = link?.querySelector('.tabular-nums')
  expect(valueEl).not.toBeNull()
  return valueEl?.textContent ?? ''
}

describe('全局运维指标条（FR-188）', () => {
  it('聚合展示五项真数据', async () => {
    renderStrip()
    await waitFor(() => {
      expect(screen.getByText('控制面在线')).toBeInTheDocument()
    })
    await waitFor(() => {
      expect(metricValue('Agent 在线')).toBe('3')
    })
    expect(metricValue('待确认')).toBe('2')
    expect(metricValue('未处理告警')).toBe('1')
    // rolling + paused = 2
    expect(metricValue('进行中变更单')).toBe('2')
  })

  it('status 失败时控制面显示异常且其它项可仍为 — 或数', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo) => {
        const url = String(input)
        if (url.includes('/admin/v1/system/status')) {
          return new Response(JSON.stringify({ code: 'err', message: 'down' }), { status: 500 })
        }
        if (url.includes('/admin/v2/agent-identities')) {
          return new Response(JSON.stringify({ items: [], total: 0 }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          })
        }
        if (url.includes('/admin/v2/servers')) {
          return new Response(
            JSON.stringify({
              items: [{ id: 1, online: true }, { id: 2, online: false }],
              total: 2,
            }),
            { status: 200, headers: { 'Content-Type': 'application/json' } },
          )
        }
        if (url.includes('/admin/v1/alert-events') || url.includes('/admin/v2/change-orders')) {
          return new Response(JSON.stringify({ code: 'err', message: 'x' }), { status: 500 })
        }
        return new Response('{}', { status: 404 })
      }),
    )
    renderStrip()
    await waitFor(() => {
      expect(screen.getByText('控制面异常')).toBeInTheDocument()
    })
    // servers 回落在线数
    await waitFor(() => {
      expect(screen.getByText('1')).toBeInTheDocument()
    })
    // 告警/变更失败 → —
    expect(screen.getAllByText('—').length).toBeGreaterThanOrEqual(1)
  })
})
