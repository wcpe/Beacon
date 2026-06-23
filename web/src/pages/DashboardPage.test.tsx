// DashboardPage 单测（FR-32 / FR-34 / FR-43）：
// 覆盖「子服/BC 两大区块拆分 → 总览卡片渲染 → 趋势图按指标渲染 → 时间窗切换重查 → CPU 不可用展示
// → 每服明细按角色分组 → BC 面板 → 无玩家名单」。
// recharts 较重且依赖容器尺寸，故把 TrendChart 替身为轻量桩，断言图按指标渲染、点数正确。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'

// 用轻量桩替身 TrendChart：暴露标题、点数、指标与该指标各点取值，规避 recharts 在 jsdom 下的尺寸/动画依赖。
// data-values 序列化所选指标各点的值（null 序列化为 "null"），供断言喂图前已把 CPU 哨兵置 null。
vi.mock('./dashboard/TrendChart', () => ({
  default: (props: {
    title: string
    metric: string
    points: Array<Record<string, number | null | string>>
  }) => (
    <div
      data-testid="trend-chart"
      data-metric={props.metric}
      data-values={JSON.stringify(props.points.map((p) => p[props.metric]))}
    >
      {props.title}（{props.points.length} 点）
    </div>
  ),
}))

// mock 后端调用，由各用例注入数据
vi.mock('../api/client', () => ({
  metricsSummary: vi.fn(),
  metricsTrend: vi.fn(),
  // FR-51：环境筛选框下拉候选来源
  listNamespaces: vi.fn(),
}))

import DashboardPage from './DashboardPage'
import { metricsSummary, metricsTrend, listNamespaces } from '../api/client'
import type { MetricsSummary, MetricsTrend } from '../api/client'

// 当前快照样例：含一个有效 CPU 平均与两服明细
const SUMMARY: MetricsSummary = {
  totalPlayers: 50,
  onlineServers: 2,
  servers: [
    { serverId: 'lobby-1', role: 'bukkit', playerCount: 42 },
    { serverId: 'pvp-2', role: 'bukkit', playerCount: 8 },
    { serverId: 'proxy-1', role: 'bungee', playerCount: 99 },
  ],
  avgTps: 19.9,
  avgMemUsed: 134217728, // 128 MB
  avgMemMax: 536870912, // 512 MB
  avgCpuLoad: 0.4,
  cpuSampleCount: 1,
  bc: {
    proxyCount: 2,
    totalConnections: 150,
    avgThreadCount: 48,
    backendUp: 3,
    backendTotal: 4,
    avgBackendLatencyMs: 12,
  },
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
  vi.mocked(listNamespaces).mockResolvedValue([{ code: 'prod', name: '生产' }])
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

  it('CPU 趋势图把无样本哨兵（avgCpuLoad=-1）置 null，不污染折线', async () => {
    // 注入含哨兵 -1 的趋势：第二点无 CPU 样本，喂图前应被置为 null（断线）而非画到 -100%。
    vi.mocked(metricsTrend).mockResolvedValue({
      points: [
        { ...TREND.points[0], avgCpuLoad: 0.3 },
        { ...TREND.points[1], avgCpuLoad: -1 },
      ],
    })
    renderPage(<DashboardPage />)
    const charts = await screen.findAllByTestId('trend-chart')
    const cpuChart = charts.find((c) => c.getAttribute('data-metric') === 'avgCpuLoad')
    expect(cpuChart).toBeDefined()
    const values = JSON.parse(cpuChart!.getAttribute('data-values') ?? '[]')
    // 有效点保留原值，哨兵 -1 被置 null；图中绝不出现 -1。
    expect(values).toEqual([0.3, null])
    expect(values).not.toContain(-1)
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

  it('整体拆「子服(bukkit)」与「BC 代理」两大区块', async () => {
    renderPage(<DashboardPage />)
    // 两大区块标题各为一个二级标题（h2），互相分离
    expect(await screen.findByRole('heading', { level: 2, name: '子服（bukkit）' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 2, name: 'BC 代理' })).toBeInTheDocument()
    // 子服区有总览卡片 + 子服明细；BC 区有 BC 面板 + BC 明细
    expect(screen.getByText('子服明细')).toBeInTheDocument()
    expect(screen.getByText('BC 明细')).toBeInTheDocument()
  })

  it('每服明细按角色分组：bukkit 进子服明细，bungee 进 BC 明细', async () => {
    renderPage(<DashboardPage />)
    // bukkit 子服落子服明细
    expect(await screen.findByText('lobby-1')).toBeInTheDocument()
    expect(screen.getByText('pvp-2')).toBeInTheDocument()
    // bungee 代理落 BC 明细（不混进子服明细）
    expect(screen.getByText('proxy-1')).toBeInTheDocument()
  })

  it('渲染 BC 代理面板（代理数 / 连接 / 线程 / 后端可达性 / 延迟）', async () => {
    renderPage(<DashboardPage />)
    expect(await screen.findByText('BC 代理')).toBeInTheDocument()
    expect(screen.getByText('在线 BC 代理数')).toBeInTheDocument()
    expect(screen.getByText('代理总连接数')).toBeInTheDocument()
    expect(screen.getByText('平均线程数')).toBeInTheDocument()
    expect(screen.getByText('后端可达性')).toBeInTheDocument()
    expect(screen.getByText('平均后端延迟')).toBeInTheDocument()
    // 连接总数与后端可达率按数据渲染
    expect(screen.getByText('150')).toBeInTheDocument()
    expect(screen.getByText('3 / 4')).toBeInTheDocument()
    expect(screen.getByText('75% 可达')).toBeInTheDocument()
    expect(screen.getByText('12 ms')).toBeInTheDocument()
  })

  it('BC 平均延迟 < 0 时展示「不可用」', async () => {
    vi.mocked(metricsSummary).mockResolvedValue({
      ...SUMMARY,
      bc: { ...SUMMARY.bc, avgBackendLatencyMs: -1 },
    })
    renderPage(<DashboardPage />)
    await screen.findByText('BC 代理')
    expect(screen.getByText('不可用')).toBeInTheDocument()
    expect(screen.getByText('无可达后端样本')).toBeInTheDocument()
  })

  it('BC 无后端时后端可达性展示「无后端」', async () => {
    vi.mocked(metricsSummary).mockResolvedValue({
      ...SUMMARY,
      bc: { ...SUMMARY.bc, backendUp: 0, backendTotal: 0, avgBackendLatencyMs: -1 },
    })
    renderPage(<DashboardPage />)
    await screen.findByText('BC 代理')
    expect(screen.getByText('无后端')).toBeInTheDocument()
    expect(screen.getByText('该代理未配置后端')).toBeInTheDocument()
  })

  it('不渲染任何玩家名单 / 身份字段（边界守护）', async () => {
    // 负向测试：故意往明细行塞名单类字段（playerNames / players），断言其值不被渲染到 DOM。
    // 唯一哨兵串便于断言；后端实际不返回这些字段，此处构造越界数据验证前端守护。
    const SENTINEL_A = '玩家甲-名单哨兵-A7F3'
    const SENTINEL_B = '玩家乙-名单哨兵-B2E9'
    const summaryWithRoster = {
      ...SUMMARY,
      servers: [
        { serverId: 'lobby-1', role: 'bukkit', playerCount: 42, playerNames: [SENTINEL_A] },
        { serverId: 'pvp-2', role: 'bukkit', playerCount: 8, players: [SENTINEL_B] },
      ],
    } as unknown as MetricsSummary
    vi.mocked(metricsSummary).mockResolvedValue(summaryWithRoster)

    const { container } = renderPage(<DashboardPage />)
    await screen.findByText('lobby-1')
    // 塞入的名单字段值绝不应出现在 DOM 中（明细表只渲染 serverId 与 playerCount）。
    expect(container.textContent).not.toContain(SENTINEL_A)
    expect(container.textContent).not.toContain(SENTINEL_B)
    // 保留原断言：名册类文案同样不得出现。
    for (const banned of ['玩家名单', '玩家列表', 'roster', 'playerNames']) {
      expect(container.textContent).not.toContain(banned)
    }
  })
})
