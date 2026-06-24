// WallboardPage 单测（FR-92）：渲染只读看板数据，且不含任何操作入口（下线 / 改派 / 编辑等）。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

// mock 后端只读查询，由用例注入数据
vi.mock('../api/client', () => ({
  metricsSummary: vi.fn(),
  listInstances: vi.fn(),
}))

import WallboardPage from './WallboardPage'
import { metricsSummary, listInstances } from '../api/client'
import type { MetricsSummary } from '../api/client'
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

// WallboardPage 仅消费 serverId / status，余字段不读，故造最小桩并断言转型。
function inst(overrides: Partial<InstanceView>): InstanceView {
  return { serverId: 'lobby-1', status: 'online', ...overrides } as InstanceView
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
  vi.mocked(listInstances).mockResolvedValue([
    inst({ serverId: 'lobby-1', status: 'online' }),
    inst({ serverId: 'lobby-2', status: 'lost' }),
  ])
})

describe('WallboardPage 只读大屏', () => {
  it('渲染只读总览数据（在线服务器数）', async () => {
    renderWallboard()
    // 总览卡片渲染后应出现在线服务器数 3
    await waitFor(() => expect(screen.getByText('42')).toBeInTheDocument())
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
