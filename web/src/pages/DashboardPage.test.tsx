// DashboardPage 单测（FR-32 / FR-34 / FR-43 + 状态墙/分角色面板/时序图重构）：
// 覆盖「集群总览条（健康计数 + 全局 KPI chips）→ 服务器状态墙（逐台瓷砖）→ 分角色面板（子服 / BC）
// → 时序监控图按指标渲染 + 时间窗切换重查 → CPU 不可用展示 → BC 字段 → 底部导航 → 无玩家名单」。
// recharts 较重且依赖容器尺寸，故把 TrendChart 与 MiniSparkline 替身为轻量桩，断言数据正确喂入而不渲染真图。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'

// 轻量桩替身 TrendChart：暴露标题、点数、指标与该指标各点取值，规避 recharts 在 jsdom 下的尺寸/动画依赖。
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

// 轻量桩替身 MiniSparkline：仅记录收到的点数，规避 recharts 尺寸依赖（不影响页面其它断言）。
vi.mock('@/components/dashboard/MiniSparkline', () => ({
  default: (props: { values: Array<number | null> }) => (
    <div data-testid="mini-sparkline" data-count={props.values.length} />
  ),
}))

// mock 后端调用，由各用例注入数据
vi.mock('../api/client', () => ({
  metricsSummary: vi.fn(),
  metricsTrend: vi.fn(),
  // FR-51：环境筛选框下拉候选来源
  listNamespaces: vi.fn(),
  // FR-64：健康分布按 status 前端计数 + 状态墙逐台渲染
  listInstances: vi.fn(),
}))

import DashboardPage from './DashboardPage'
import { metricsSummary, metricsTrend, listNamespaces, listInstances } from '../api/client'
import type { MetricsSummary, MetricsTrend } from '../api/client'
import type { InstanceView } from '../api/types'

// 在册实例样例工厂：状态墙逐台渲染会读 role/status/tps/playerCount/proxy.*，故造较完整桩。
function inst(overrides: Partial<InstanceView>): InstanceView {
  return {
    namespace: 'prod',
    serverId: 'lobby-1',
    role: 'bukkit',
    group: 'area1',
    zone: 'z1',
    assigned: true,
    address: '10.0.0.1:25565',
    version: '1.0',
    agentVersion: '',
    status: 'online',
    capacity: 0,
    weight: 0,
    metadata: {},
    lastHeartbeat: '',
    lastHeartbeatAgeSec: 0,
    healthReason: '',
    appliedMd5: '',
    playerCount: 0,
    tps: 20,
    backends: [],
    zoneDefaultEntry: false,
    proxy: {
      onlineConnections: 0,
      threadCount: 0,
      uptimeMs: 0,
      backendUp: 0,
      backendTotal: 0,
      backendAvgLatencyMs: -1,
    },
    registeredAt: '',
    ...overrides,
  }
}

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
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.mocked(metricsSummary).mockResolvedValue(SUMMARY)
  vi.mocked(metricsTrend).mockResolvedValue(TREND)
  vi.mocked(listNamespaces).mockResolvedValue([{ code: 'prod', name: '生产' }])
  vi.mocked(listInstances).mockResolvedValue([
    inst({ serverId: 'lobby-1', status: 'online' }),
    inst({ serverId: 'pvp-2', status: 'online' }),
    inst({ serverId: 'lost-1', status: 'lost' }),
  ])
})

describe('DashboardPage', () => {
  it('集群总览条渲染全局 KPI chips（玩家 / 均TPS / 均CPU / 均内存）', async () => {
    renderPage(<DashboardPage />)
    // 玩家总数标签同时出现在 KPI chip 与子服面板（断言至少一处）
    expect((await screen.findAllByText('总在线玩家数')).length).toBeGreaterThanOrEqual(1)
    // 以下数值在 KPI chip 与子服面板各出现一次（共两处）：玩家 50、CPU 40%、内存 128 MB。
    expect(screen.getAllByText('50').length).toBeGreaterThanOrEqual(2)
    expect(screen.getAllByText('40%').length).toBeGreaterThanOrEqual(2)
    expect(screen.getAllByText('128 MB').length).toBeGreaterThanOrEqual(2)
  })

  it('集群总览条渲染健康三态计数（在线/亚健康/失联/离线）', async () => {
    renderPage(<DashboardPage />)
    // listInstances mock：2 online / 0 degraded / 1 lost / 0 offline
    expect(await screen.findByText('online 2')).toBeInTheDocument()
    expect(screen.getByText('degraded 0')).toBeInTheDocument()
    expect(screen.getByText('lost 1')).toBeInTheDocument()
    expect(screen.getByText('offline 0')).toBeInTheDocument()
  })

  it('服务器状态墙逐台渲染瓷砖（在册实例 serverId 出现在状态墙）', async () => {
    renderPage(<DashboardPage />)
    // 状态墙标题 + 三台在册实例瓷砖（serverId 现在恰恰应出现在状态墙）
    expect(await screen.findByText('服务器状态墙')).toBeInTheDocument()
    expect(screen.getByText('lobby-1')).toBeInTheDocument()
    expect(screen.getByText('pvp-2')).toBeInTheDocument()
    expect(screen.getByText('lost-1')).toBeInTheDocument()
  })

  it('按四个指标渲染时序监控图，点数对齐趋势数据', async () => {
    renderPage(<DashboardPage />)
    const charts = await screen.findAllByTestId('trend-chart')
    expect(charts).toHaveLength(4)
    const metrics = charts.map((c) => c.getAttribute('data-metric')).sort()
    expect(metrics).toEqual(['avgCpuLoad', 'avgMemUsed', 'avgTps', 'totalPlayers'])
    // 每图收到 2 个时间序列点
    expect(screen.getAllByText(/（2 点）/).length).toBe(4)
  })

  it('CPU 时序图把无样本哨兵（avgCpuLoad=-1）置 null，不污染折线', async () => {
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

  it('avgCpuLoad < 0 时分角色面板 CPU 展示「不可用」', async () => {
    vi.mocked(metricsSummary).mockResolvedValue({
      ...SUMMARY,
      avgCpuLoad: -1,
      cpuSampleCount: 0,
    })
    renderPage(<DashboardPage />)
    expect(await screen.findByText('无可用 CPU 样本')).toBeInTheDocument()
  })

  it('分角色面板：子服与 BC 两面板标题各为一个二级标题', async () => {
    renderPage(<DashboardPage />)
    expect(await screen.findByRole('heading', { level: 2, name: '子服（bukkit）' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 2, name: 'BC 代理' })).toBeInTheDocument()
  })

  it('BC 面板渲染关键字段（代理数 / 连接 / 线程 / 后端可达性 / 延迟）', async () => {
    renderPage(<DashboardPage />)
    expect(await screen.findByText('在线 BC 代理数')).toBeInTheDocument()
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
    await screen.findByRole('heading', { level: 2, name: 'BC 代理' })
    expect(screen.getByText('不可用')).toBeInTheDocument()
    expect(screen.getByText('无可达后端样本')).toBeInTheDocument()
  })

  it('BC 无后端时后端可达性展示「无后端」', async () => {
    vi.mocked(metricsSummary).mockResolvedValue({
      ...SUMMARY,
      bc: { ...SUMMARY.bc, backendUp: 0, backendTotal: 0, avgBackendLatencyMs: -1 },
    })
    renderPage(<DashboardPage />)
    await screen.findByRole('heading', { level: 2, name: 'BC 代理' })
    expect(screen.getByText('无后端')).toBeInTheDocument()
    expect(screen.getByText('该代理未配置后端')).toBeInTheDocument()
  })

  // FR-63：环境筛选选某环境后可一键清回「全部聚合」（空值）。
  it('选环境后可一键清回全部聚合（清空按钮置空 namespace）', async () => {
    renderPage(<DashboardPage />)
    const ns = await screen.findByLabelText('环境')
    // 选中环境 prod（候选显示「编码 · 名称」，FR-70）
    await userEvent.click(ns)
    await userEvent.click(await screen.findByRole('option', { name: 'prod · 生产' }))
    // 选中后按 prod 查询
    await waitFor(() => expect(vi.mocked(metricsSummary)).toHaveBeenCalledWith('prod'))
    // 一键清空：点清空按钮，namespace 回空 → 按聚合全部（undefined）重查
    await userEvent.click(screen.getByLabelText('清空环境筛选'))
    await waitFor(() => expect(vi.mocked(metricsSummary)).toHaveBeenCalledWith(undefined))
    // 输入框已清空回显
    expect(screen.getByLabelText('环境')).toHaveValue('')
  })

  // FR-64：底部「服务器详情 → /servers · 拓扑 → /topology」链接。
  it('底部含服务器详情与拓扑导航链接', async () => {
    renderPage(<DashboardPage />)
    const serversLink = await screen.findByText('服务器详情 → /servers')
    expect(serversLink.closest('a')).toHaveAttribute('href', '/servers')
    const topoLink = screen.getByText('拓扑 → /topology')
    expect(topoLink.closest('a')).toHaveAttribute('href', '/topology')
  })

  it('不渲染任何玩家名单 / 身份字段（边界守护）', async () => {
    // 负向测试：故意往 servers 行塞名单类字段（playerNames / players），断言其值不被渲染到 DOM。
    // 看板按 role 计数 / 按状态计数 / 状态墙仅展示负载数字，名单无从泄露；此处构造越界数据验证前端守护。
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
    // 锚定页面就绪（状态墙标题渲染）后再断言名单未泄露。
    await screen.findByText('服务器状态墙')
    // 塞入的名单字段值绝不应出现在 DOM 中。
    expect(container.textContent).not.toContain(SENTINEL_A)
    expect(container.textContent).not.toContain(SENTINEL_B)
    // 名册类文案同样不得出现。
    for (const banned of ['玩家名单', '玩家列表', 'roster', 'playerNames']) {
      expect(container.textContent).not.toContain(banned)
    }
  })

  it('集群总览条使用分段健康条 + KPI chips（不再用低密度大数字卡）', async () => {
    renderPage(<DashboardPage />)
    await screen.findByText('服务器状态墙')
    // 内嵌迷你趋势 sparkline 桩出现（分角色面板各一条），证明面板按新结构渲染。
    const sparks = screen.getAllByTestId('mini-sparkline')
    expect(sparks.length).toBeGreaterThanOrEqual(2)
  })
})
