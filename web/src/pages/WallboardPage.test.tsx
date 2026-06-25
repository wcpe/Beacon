// WallboardPage 单测（FR-92）：状态墙 + 大趋势重构后，渲染只读看板数据，且不含任何操作入口（下线 / 改派 / 编辑等）。
// recharts 较重且依赖容器尺寸，故把底部大趋势 TrendChart 替身为轻量桩。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

// 轻量桩替身底部大趋势 TrendChart：仅记录收到点数，规避 recharts 尺寸依赖。
vi.mock('./dashboard/TrendChart', () => ({
  default: (props: { title: string; points: unknown[] }) => (
    <div data-testid="trend-chart">
      {props.title}（{props.points.length} 点）
    </div>
  ),
}))

// mock 后端只读查询，由用例注入数据
vi.mock('../api/client', () => ({
  metricsSummary: vi.fn(),
  listInstances: vi.fn(),
  metricsTrend: vi.fn(),
}))

import WallboardPage from './WallboardPage'
import { metricsSummary, listInstances, metricsTrend } from '../api/client'
import type { MetricsSummary, MetricsTrend } from '../api/client'
import type { InstanceView } from '../api/types'

const SUMMARY: MetricsSummary = {
  totalPlayers: 42,
  onlineServers: 3,
  servers: [
    { serverId: 'lobby-1', role: 'bukkit', playerCount: 10 },
    { serverId: 'proxy-1', role: 'bungee', playerCount: 0 },
  ],
  avgTps: 19.8,
  avgMemUsed: 1024,
  avgMemMax: 2048,
  avgCpuLoad: 0.25,
  cpuSampleCount: 5,
  bc: {
    proxyCount: 1,
    totalConnections: 12,
    avgThreadCount: 4,
    backendUp: 2,
    backendTotal: 2,
    avgBackendLatencyMs: 8,
  },
}

const TREND: MetricsTrend = {
  points: [
    { sampledAt: '2026-06-20T08:00:00Z', totalPlayers: 40, avgTps: 19.8, avgMemUsed: 1024, avgMemMax: 2048, avgCpuLoad: 0.2 },
    { sampledAt: '2026-06-20T08:05:00Z', totalPlayers: 42, avgTps: 19.9, avgMemUsed: 1024, avgMemMax: 2048, avgCpuLoad: 0.25 },
  ],
}

// 状态墙逐台渲染会读 role/status/tps/playerCount/proxy.*，故造较完整桩。
function inst(overrides: Partial<InstanceView>): InstanceView {
  return {
    namespace: 'prod',
    serverId: 'lobby-1',
    role: 'bukkit',
    group: 'g',
    zone: null,
    assigned: false,
    address: '',
    version: '',
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
    proxy: { onlineConnections: 0, threadCount: 0, uptimeMs: 0, backendUp: 0, backendTotal: 0, backendAvgLatencyMs: -1 },
    registeredAt: '',
    ...overrides,
  }
}

function renderWallboard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <WallboardPage />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.mocked(metricsSummary).mockResolvedValue(SUMMARY)
  vi.mocked(metricsTrend).mockResolvedValue(TREND)
  vi.mocked(listInstances).mockResolvedValue([
    inst({ serverId: 'lobby-1', status: 'online' }),
    inst({ serverId: 'lobby-2', status: 'lost' }),
  ])
})

describe('WallboardPage 只读大屏', () => {
  it('渲染只读总览数据（玩家总数巨号）', async () => {
    renderWallboard()
    // 顶部巨号玩家总数 42
    await waitFor(() => expect(screen.getByText('42')).toBeInTheDocument())
  })

  it('渲染服务器状态墙（逐台瓷砖 serverId）', async () => {
    renderWallboard()
    await waitFor(() => expect(screen.getByText('服务器状态墙')).toBeInTheDocument())
    expect(screen.getByText('lobby-1')).toBeInTheDocument()
    expect(screen.getByText('lobby-2')).toBeInTheDocument()
  })

  it('渲染底部一条大趋势（在线玩家）', async () => {
    renderWallboard()
    // TrendChart 桩出现且收到趋势点
    await waitFor(() => expect(screen.getByTestId('trend-chart')).toBeInTheDocument())
    expect(screen.getByText(/（2 点）/)).toBeInTheDocument()
  })

  it('不含任何操作入口（下线 / 改派 / 编辑等按钮）', async () => {
    renderWallboard()
    await waitFor(() => expect(screen.getByText('42')).toBeInTheDocument())
    // 大屏纯只读：不应出现任何 button / 操作类文案
    expect(screen.queryAllByRole('button')).toHaveLength(0)
    for (const label of ['下线', '改派', 'drain', '编辑', '删除', '保存']) {
      expect(screen.queryByText(label)).not.toBeInTheDocument()
    }
  })
})
