// ServiceAnalysisPage 单测（FR-73）：
// 覆盖「KPI 卡片（总数 / 成功 / 失败 / 成功率，含 total=0 不除零）→ 按动作排行（降序）→ 每日趋势点数
// → 切时间窗 Tabs 以新 from/to 重查 → 切环境带 namespace 重查」。
// recharts 较重且依赖容器尺寸，故把两个图表替身为轻量桩，断言数据正确喂入而不渲染真图。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'

// 轻量桩替身两个图表：暴露收到的点 / 条目数与序列化数据，规避 recharts 在 jsdom 下的尺寸/动画依赖。
vi.mock('./service-analysis/DayTrendChart', () => ({
  default: (props: { points: Array<{ date: string; count: number }> }) => (
    <div
      data-testid="day-trend"
      data-count={props.points.length}
      data-values={JSON.stringify(props.points.map((p) => p.count))}
    />
  ),
}))
vi.mock('./service-analysis/ActionRankChart', () => ({
  default: (props: { items: Array<{ action: string; label: string; count: number }> }) => (
    <div
      data-testid="action-rank"
      data-count={props.items.length}
      data-counts={JSON.stringify(props.items.map((i) => i.count))}
      data-labels={JSON.stringify(props.items.map((i) => i.label))}
    />
  ),
}))

// mock 后端调用，由各用例注入数据
vi.mock('../api/client', () => ({
  getAuditAnalytics: vi.fn(),
  // 环境筛选框下拉候选来源
  listNamespaces: vi.fn(),
}))

import ServiceAnalysisPage from './ServiceAnalysisPage'
import { getAuditAnalytics, listNamespaces } from '../api/client'
import type { AuditAnalytics } from '../api/types'

// 聚合样例：128 总 / 119 成功 / 9 失败；按动作两条；每日趋势三点。
const ANALYTICS: AuditAnalytics = {
  from: '2026-05-25T00:00:00Z',
  to: '2026-06-24T00:00:00Z',
  total: 128,
  okCount: 119,
  failCount: 9,
  byAction: [
    { action: 'config.publish', count: 40 },
    { action: 'zone.assign', count: 22 },
  ],
  byDay: [
    { date: '2026-06-01', count: 5 },
    { date: '2026-06-02', count: 8 },
    { date: '2026-06-03', count: 3 },
  ],
}

function renderPage(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.mocked(getAuditAnalytics).mockResolvedValue(ANALYTICS)
  vi.mocked(listNamespaces).mockResolvedValue([{ code: 'prod', name: '生产' }])
})

describe('ServiceAnalysisPage', () => {
  it('渲染 KPI 卡片（总数 / 成功 / 失败 / 成功率）', async () => {
    renderPage(<ServiceAnalysisPage />)
    expect(await screen.findByText('总操作数')).toBeInTheDocument()
    expect(screen.getByText('成功数')).toBeInTheDocument()
    expect(screen.getByText('失败数')).toBeInTheDocument()
    expect(screen.getByText('成功率')).toBeInTheDocument()
    // 数值按聚合渲染
    expect(screen.getByText('128')).toBeInTheDocument()
    expect(screen.getByText('119')).toBeInTheDocument()
    expect(screen.getByText('9')).toBeInTheDocument()
    // 成功率 = okCount / total = 119/128 ≈ 93.0%
    expect(screen.getByText('93.0%')).toBeInTheDocument()
  })

  it('total=0 时成功率显示 0% 且不崩（不除零）', async () => {
    vi.mocked(getAuditAnalytics).mockResolvedValue({
      ...ANALYTICS,
      total: 0,
      okCount: 0,
      failCount: 0,
      byAction: [],
      byDay: [],
    })
    renderPage(<ServiceAnalysisPage />)
    expect(await screen.findByText('成功率')).toBeInTheDocument()
    expect(screen.getByText('0%')).toBeInTheDocument()
  })

  it('按动作排行按 count 降序喂入图表，action 映射中文', async () => {
    renderPage(<ServiceAnalysisPage />)
    const rank = await screen.findByTestId('action-rank')
    expect(rank.getAttribute('data-count')).toBe('2')
    // 计数保持降序（后端已降序，前端原样透传）
    expect(JSON.parse(rank.getAttribute('data-counts') ?? '[]')) .toEqual([40, 22])
    // action 经既有审计 i18n 映射为中文
    expect(JSON.parse(rank.getAttribute('data-labels') ?? '[]')).toEqual(['发布配置', '指派区'])
  })

  it('每日趋势点数与计数对齐聚合数据', async () => {
    renderPage(<ServiceAnalysisPage />)
    const trend = await screen.findByTestId('day-trend')
    expect(trend.getAttribute('data-count')).toBe('3')
    expect(JSON.parse(trend.getAttribute('data-values') ?? '[]')).toEqual([5, 8, 3])
  })

  it('默认以近 30 天窗口查询，切到近 7 天以更近的 from 重查', async () => {
    renderPage(<ServiceAnalysisPage />)
    // 初次查询：默认近 30 天
    await waitFor(() => expect(vi.mocked(getAuditAnalytics)).toHaveBeenCalled())
    const firstFrom = vi.mocked(getAuditAnalytics).mock.calls[0][0].from as string
    expect(firstFrom).toMatch(/^\d{4}-\d{2}-\d{2}T/)
    // 切近 7 天：以更近（更大）的 from 重查
    await userEvent.click(screen.getByRole('tab', { name: '近 7 天' }))
    await waitFor(() => {
      const calls = vi.mocked(getAuditAnalytics).mock.calls
      const lastFrom = calls[calls.length - 1][0].from as string
      expect(new Date(lastFrom).getTime()).toBeGreaterThan(new Date(firstFrom).getTime())
    })
  })

  it('切环境带 namespace 重查；一键清空回聚合全部', async () => {
    renderPage(<ServiceAnalysisPage />)
    const ns = await screen.findByLabelText('环境')
    await userEvent.click(ns)
    await userEvent.click(await screen.findByRole('option', { name: 'prod · 生产' }))
    // 选中后带 namespace=prod 重查
    await waitFor(() =>
      expect(vi.mocked(getAuditAnalytics)).toHaveBeenCalledWith(
        expect.objectContaining({ namespace: 'prod' }),
      ),
    )
    // 一键清空：namespace 回 undefined（聚合全部）
    await userEvent.click(screen.getByLabelText('清空环境筛选'))
    await waitFor(() =>
      expect(vi.mocked(getAuditAnalytics)).toHaveBeenCalledWith(
        expect.objectContaining({ namespace: undefined }),
      ),
    )
  })
})
