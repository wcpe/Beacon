// AlertEventsPage 冒烟测试（FR-89）：验证时间线渲染、级别过滤透传、空态文案。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'

vi.mock('../api/client', () => ({
  listAlertEvents: vi.fn(),
  listNamespaces: vi.fn(),
}))

import AlertEventsPage from './AlertEventsPage'
import { listAlertEvents, listNamespaces } from '../api/client'

const EMPTY_PAGE = { total: 0, items: [] }

function eventRow(over: Partial<import('../api/types').AlertEventView> = {}) {
  return {
    id: 1,
    type: 'health-transition',
    level: 'critical',
    serverId: 'lobby-1',
    namespace: 'prod',
    message: 'lobby-1 online → lost',
    detail: '{"status":"lost"}',
    createdAt: '2026-06-20T08:00:00Z',
    ...over,
  }
}

function renderPage(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.mocked(listAlertEvents).mockResolvedValue(EMPTY_PAGE)
  vi.mocked(listNamespaces).mockResolvedValue([{ code: 'prod', name: '生产' }])
})

describe('AlertEventsPage', () => {
  it('空态显示「无告警事件」', async () => {
    renderPage(<AlertEventsPage />)
    expect(await screen.findByText('无告警事件')).toBeInTheDocument()
  })

  it('渲染事件时间线条目（含摘要与类型中文标签）', async () => {
    vi.mocked(listAlertEvents).mockResolvedValue({ total: 1, items: [eventRow()] })
    renderPage(<AlertEventsPage />)
    // 摘要文案唯一，出现在时间线条目
    expect(await screen.findByText('lobby-1 online → lost')).toBeInTheDocument()
    // 类型 / 级别英文枚举经 i18n 映射为中文（下拉选项与时间线 Badge 都含，故至少一处）
    expect(screen.getAllByText('健康流转').length).toBeGreaterThan(0)
    expect(screen.getAllByText('严重').length).toBeGreaterThan(0)
  })

  it('选级别并查询时把 level 透传给 listAlertEvents', async () => {
    renderPage(<AlertEventsPage />)
    // 打开级别下拉并选「严重」
    const levelTrigger = await screen.findByLabelText('级别')
    await userEvent.click(levelTrigger)
    await userEvent.click(await screen.findByRole('option', { name: '严重' }))
    await userEvent.click(screen.getByRole('button', { name: '查询' }))
    await waitFor(() =>
      expect(vi.mocked(listAlertEvents)).toHaveBeenCalledWith(
        expect.objectContaining({ level: 'critical' }),
      ),
    )
  })
})
