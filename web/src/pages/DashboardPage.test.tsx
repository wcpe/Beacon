// DashboardPage 单测（FR-32）：
// 覆盖「总览卡片渲染 → 趋势图按指标渲染 → 时间窗切换重查 → CPU 不可用展示 → 每服明细 → 无玩家名单」。
// recharts 较重且依赖容器尺寸，故把 TrendChart 替身为轻量桩，断言图按指标渲染、点数正确。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'

// 用轻量桩替身 TrendChart：暴露标题、点数与指标，规避 recharts 在 jsdom 下的尺寸/动画依赖
vi.mock('./dashboard/TrendChart', () => ({
  default: (props: { title: string; metric: string; points: unknown[] }) => (
    <div data-testid="trend-chart" data-metric={props.metric}>
      {props.title}（{props.points.length} 点）
    </div>
  ),
}))

// mock 后端调用，由各用例注入数据
vi.mock('../api/client', () => ({
  metricsSummary: vi.fn(),
  metricsTrend: vi.fn(),
}))

import DashboardPage from './DashboardPage'
import { metricsSummary, metricsTrend } from '../api/client'
import type { MetricsSummary, MetricsTrend } from '../api/client'

// 当前快照样例：含一个有效 CPU 平均与两服明细
const SUMMARY: MetricsSummary = {
  totalPlayers: 50,
  onlineServers: 2,
  servers: [
    { serverId: 'lobby-1', playerCount: 42 },
    { serverId: 'pvp-2', playerCount: 8 },
  ],
  avgTps: 19.9,
  avgMemUsed: 134217728, // 128 MB
  avgMemMax: 536870912, // 512 MB
  avgCpuLoad: 0.4,
  cpuSampleCount: 1,
}

// 趋势样例：两个时间序列点
const TREND: MetricsTrend = {
  points: [
    {
      sampledAt: '2026-06-20T08:00:00Z',
      totalPlayers: 48,
      avgTps: 19.8,
      avgMemUsed: 130000000,
      avgMemMax: 536870912,
      avgCpuLoad: 0.3,
    },
    {
      sampledAt: '2026-06-20T08:05:00Z',
      totalPlayers: 50,
      avgTps: 19.9,
      avgMemUsed: 134217728,
      avgMemMax: 536870912,
      avgCpuLoad: 0.4,
    },
  ],
}

function renderPage(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.mocked(metricsSummary).mockResolvedValue(SUMMARY)
  vi.mocked(metricsTrend).mockResolvedValue(TREND)
})

describe('DashboardPage', () => {
  it('渲染总览卡片（总人数 / 在线服务器 / 平均 TPS / 内存 / CPU）', async () => {
    renderPage(<DashboardPage />)
    expect(await screen.findByText('总在线玩家数')).toBeInTheDocument()
    expect(screen.getByText('在线服务器数')).toBeInTheDocument()
    expect(screen.getByText('平均 TPS')).toBeInTheDocument()
    expect(screen.getByText('平均内存')).toBeInTheDocument()
    expect(screen.getByText('平均 CPU 负载')).toBeInTheDocument()
    // 内存按人类可读字节展示（128 MB used，512 MB max）
    expect(screen.getByText('128 MB')).toBeInTheDocument()
    expect(screen.getByText('最大 512 MB')).toBeInTheDocument()
    // CPU 有效样本：显示百分比与样本数
    expect(screen.getByText('40%')).toBeInTheDocument()
  })

  it('按四个指标渲染趋势图，点数对齐趋势数据', async () => {
    renderPage(<DashboardPage />)
    const charts = await screen.findAllByTestId('trend-chart')
    expect(charts).toHaveLength(4)
    const metrics = charts.map((c) => c.getAttribute('data-metric')).sort()
    expect(metrics).toEqual(['avgCpuLoad', 'avgMemUsed', 'avgTps', 'totalPlayers'])
    // 每图收到 2 个时间序列点
    expect(screen.getAllByText(/（2 点）/).length).toBe(4)
  })

  it('切换时间窗触发趋势重查（默认 1h → 24h）', async () => {
    renderPage(<DashboardPage />)
    await screen.findByText('历史趋势')
    // 初次以 window=1h 查询
    await waitFor(() =>
      expect(vi.mocked(metricsTrend)).toHaveBeenCalledWith(
        expect.objectContaining({ window: '1h' }),
      ),
    )
    await userEvent.click(screen.getByRole('tab', { name: '近 24 小时' }))
    await waitFor(() =>
      expect(vi.mocked(metricsTrend)).toHaveBeenCalledWith(
        expect.objectContaining({ window: '24h' }),
      ),
    )
  })

  it('avgCpuLoad < 0 时 CPU 展示「不可用」', async () => {
    vi.mocked(metricsSummary).mockResolvedValue({
      ...SUMMARY,
      avgCpuLoad: -1,
      cpuSampleCount: 0,
    })
    renderPage(<DashboardPage />)
    expect(await screen.findByText('不可用')).toBeInTheDocument()
    expect(screen.getByText('无可用 CPU 样本')).toBeInTheDocument()
  })

  it('每服明细按 serverId → 在线人数渲染', async () => {
    renderPage(<DashboardPage />)
    expect(await screen.findByText('lobby-1')).toBeInTheDocument()
    expect(screen.getByText('pvp-2')).toBeInTheDocument()
    expect(screen.getByText('每服明细')).toBeInTheDocument()
  })

  it('不渲染任何玩家名单 / 身份字段（边界守护）', async () => {
    const { container } = renderPage(<DashboardPage />)
    await screen.findByText('lobby-1')
    // 看板只展示聚合数字，不得出现名单类文案
    for (const banned of ['玩家名单', '玩家列表', 'roster', 'playerNames']) {
      expect(container.textContent).not.toContain(banned)
    }
  })
})
