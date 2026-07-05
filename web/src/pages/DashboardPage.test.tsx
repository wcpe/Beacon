// DashboardPage 单测（FR-137）：覆盖一屏高密度看板、服务器明细过滤、全局环境联动与名单字段不泄露。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'

vi.mock('@beacon/ui', async () => {
  const actual = await vi.importActual<typeof import('@beacon/ui')>('@beacon/ui')
  return {
    ...actual,
    MiniSparkline: (props: { values: Array<number | null> }) => (
      <div data-testid="mini-sparkline" data-count={props.values.length} />
    ),
  }
})

vi.mock('../api/client', () => ({
  metricsSummary: vi.fn(),
  metricsTrend: vi.fn(),
  listNamespaces: vi.fn(),
  listInstances: vi.fn(),
}))

import DashboardPage from './DashboardPage'
import { metricsSummary, metricsTrend, listNamespaces, listInstances } from '../api/client'
import type { MetricsSummary, MetricsTrend } from '../api/client'
import type { InstanceView } from '../api/types'
import { setEnvironment } from '@/state/environment'

function inst(overrides: Partial<InstanceView>): InstanceView {
  return {
    namespace: 'prod',
    serverId: 'lobby-1',
    role: 'bukkit',
    group: 'BC-HZ',
    zone: 'A-01',
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
    appliedMd5: 'cfg-001',
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

const SUMMARY: MetricsSummary = {
  totalPlayers: 50,
  onlineServers: 2,
  servers: [
    { serverId: 'lobby-1', role: 'bukkit', playerCount: 42 },
    { serverId: 'pvp-2', role: 'bukkit', playerCount: 8 },
    { serverId: 'proxy-1', role: 'bungee', playerCount: 99 },
  ],
  avgTps: 19.9,
  avgMemUsed: 134217728,
  avgMemMax: 536870912,
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
  setEnvironment('')
  vi.mocked(metricsSummary).mockResolvedValue(SUMMARY)
  vi.mocked(metricsTrend).mockResolvedValue(TREND)
  vi.mocked(listNamespaces).mockResolvedValue([{ code: 'prod', name: '生产' }])
  vi.mocked(listInstances).mockResolvedValue([
    inst({ serverId: 'lobby-1', status: 'online', playerCount: 42, metadata: { cpu: '32%', mem: '45%' } }),
    inst({ serverId: 'pvp-2', status: 'online', playerCount: 8, address: '10.0.0.2:25565' }),
    inst({ serverId: 'lost-1', status: 'lost', healthReason: 'Agent 心跳超时', address: '10.0.0.3:25565' }),
  ])
})

describe('DashboardPage', () => {
  it('渲染一屏高密度看板的 KPI 矩阵与三块中部面板', async () => {
    renderPage(<DashboardPage />)

    expect(await screen.findByText('实例健康')).toBeInTheDocument()
    expect(screen.getByText('Agent 连接率')).toBeInTheDocument()
    expect(screen.getByText('SSE 推送流')).toBeInTheDocument()
    expect(screen.getByText('集群健康矩阵')).toBeInTheDocument()
    expect(screen.getByText('实时任务')).toBeInTheDocument()
    expect(screen.getByText('最近异常')).toBeInTheDocument()
    expect(screen.getByText('服务器明细')).toBeInTheDocument()
    expect(screen.getAllByTestId('mini-sparkline').length).toBeGreaterThanOrEqual(6)
  })

  it('KPI 与异常面板基于实例状态展示在线、失联与告警原因', async () => {
    renderPage(<DashboardPage />)

    expect(await screen.findByText('在线 2 / 失联 1 / 离线 0')).toBeInTheDocument()
    expect(screen.getByText('Agent 心跳超时')).toBeInTheDocument()
    expect(screen.getByText('未恢复')).toBeInTheDocument()
  })

  it('服务器明细表支持按 serverId / IP 搜索过滤', async () => {
    const user = userEvent.setup()
    renderPage(<DashboardPage />)

    await screen.findByText('共 3 条')
    await user.type(screen.getByLabelText('搜索服务器明细'), 'lost')

    expect(await screen.findByText('共 1 条')).toBeInTheDocument()
    expect(screen.getByText('lost-1')).toBeInTheDocument()
  })

  it('全局环境切换驱动看板按该环境 / 全部聚合重查', async () => {
    renderPage(<DashboardPage />)

    await waitFor(() => expect(vi.mocked(metricsSummary)).toHaveBeenCalledWith(undefined))
    await waitFor(() =>
      expect(vi.mocked(metricsTrend)).toHaveBeenCalledWith(
        expect.objectContaining({ namespace: undefined, window: '1h' }),
      ),
    )

    setEnvironment('prod')
    await waitFor(() => expect(vi.mocked(metricsSummary)).toHaveBeenCalledWith('prod'))
    await waitFor(() => expect(vi.mocked(listInstances)).toHaveBeenCalledWith({ namespace: 'prod' }))

    setEnvironment('')
    await waitFor(() => expect(vi.mocked(metricsSummary)).toHaveBeenCalledWith(undefined))
  })

  it('不渲染任何玩家名单 / 身份字段', async () => {
    const sentinelA = '玩家甲-名单哨兵-A7F3'
    const sentinelB = '玩家乙-名单哨兵-B2E9'
    vi.mocked(metricsSummary).mockResolvedValue({
      ...SUMMARY,
      servers: [
        { serverId: 'lobby-1', role: 'bukkit', playerCount: 42, playerNames: [sentinelA] },
        { serverId: 'pvp-2', role: 'bukkit', playerCount: 8, players: [sentinelB] },
      ],
    } as unknown as MetricsSummary)

    const { container } = renderPage(<DashboardPage />)
    await screen.findByText('服务器明细')

    expect(container.textContent).not.toContain(sentinelA)
    expect(container.textContent).not.toContain(sentinelB)
    for (const banned of ['玩家名单', '玩家列表', 'roster', 'playerNames']) {
      expect(container.textContent).not.toContain(banned)
    }
  })
})
