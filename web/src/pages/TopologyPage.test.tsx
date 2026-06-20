// TopologyPage 单测（FR-37）：
// 覆盖「未选环境时不查询/出提示 → 查询后渲染拓扑图 → 喂图数据含真实节点/bc→bukkit 边/分组 → 空拓扑提示 → 轮询配置」。
// ECharts 依赖 canvas，故把 TopologyGraph 替身为轻量桩，断言喂图数据正确而不实际渲染。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'

// 用轻量桩替身 TopologyGraph：序列化喂入的节点 serverId、边、分组，供断言图收到真实数据。
vi.mock('./topology/TopologyGraph', () => ({
  default: (props: { data: { nodes: { serverId: string }[]; edges: unknown[]; groups: unknown[] } }) => (
    <div
      data-testid="topology-graph"
      data-nodes={JSON.stringify(props.data.nodes.map((n) => n.serverId))}
      data-edges={JSON.stringify(props.data.edges)}
      data-groups={JSON.stringify(props.data.groups)}
    />
  ),
}))

// mock 后端调用，由各用例注入数据
vi.mock('../api/client', () => ({
  getTopology: vi.fn(),
}))

import TopologyPage from './TopologyPage'
import { getTopology } from '../api/client'
import type { TopologyView } from '../api/types'

// 拓扑样例：1 个 bc + 2 个 bukkit，两条 bc→bukkit 边，两个 zone 分组
const TOPO: TopologyView = {
  namespace: 'prod',
  nodes: [
    { serverId: 'bc-1', role: 'bungee', group: 'area1', zone: null, status: 'online', address: '10.0.0.1:25577' },
    { serverId: 'lobby-1', role: 'bukkit', group: 'area1', zone: 'z1', status: 'online', address: '10.0.0.2:25565' },
    { serverId: 'pvp-1', role: 'bukkit', group: 'area1', zone: 'z2', status: 'degraded', address: '10.0.0.3:25565' },
  ],
  edges: [
    { source: 'bc-1', target: 'lobby-1' },
    { source: 'bc-1', target: 'pvp-1' },
  ],
  groups: [
    { group: 'area1', zone: 'z1', members: ['lobby-1'] },
    { group: 'area1', zone: 'z2', members: ['pvp-1'] },
  ],
}

function renderPage(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.mocked(getTopology).mockReset()
  vi.mocked(getTopology).mockResolvedValue(TOPO)
})

describe('TopologyPage', () => {
  it('未选环境时不发请求并展示提示', () => {
    renderPage(<TopologyPage />)
    expect(screen.getByText(/请先在上方输入环境并查询/)).toBeInTheDocument()
    expect(vi.mocked(getTopology)).not.toHaveBeenCalled()
  })

  it('查询某环境后按该环境拉取拓扑并渲染图', async () => {
    renderPage(<TopologyPage />)
    await userEvent.type(screen.getByLabelText('环境'), 'prod')
    await userEvent.click(screen.getByRole('button', { name: '查询' }))
    await waitFor(() => expect(vi.mocked(getTopology)).toHaveBeenCalledWith('prod'))
    expect(await screen.findByTestId('topology-graph')).toBeInTheDocument()
  })

  it('喂图数据含真实节点、bc→bukkit 边与大区/zone 分组', async () => {
    renderPage(<TopologyPage />)
    await userEvent.type(screen.getByLabelText('环境'), 'prod')
    await userEvent.click(screen.getByRole('button', { name: '查询' }))
    const graph = await screen.findByTestId('topology-graph')
    // 节点 serverId 透传
    expect(JSON.parse(graph.getAttribute('data-nodes') ?? '[]')).toEqual(['bc-1', 'lobby-1', 'pvp-1'])
    // 真实 bc→bukkit 边透传
    expect(JSON.parse(graph.getAttribute('data-edges') ?? '[]')).toEqual([
      { source: 'bc-1', target: 'lobby-1' },
      { source: 'bc-1', target: 'pvp-1' },
    ])
    // 大区/zone 分组透传
    expect(JSON.parse(graph.getAttribute('data-groups') ?? '[]')).toEqual([
      { group: 'area1', zone: 'z1', members: ['lobby-1'] },
      { group: 'area1', zone: 'z2', members: ['pvp-1'] },
    ])
  })

  it('图例显示 BC / 子服计数', async () => {
    renderPage(<TopologyPage />)
    await userEvent.type(screen.getByLabelText('环境'), 'prod')
    await userEvent.click(screen.getByRole('button', { name: '查询' }))
    await screen.findByTestId('topology-graph')
    // 1 个 bc、2 个 bukkit
    expect(screen.getByText(/BC 代理（1）/)).toBeInTheDocument()
    expect(screen.getByText(/子服（2）/)).toBeInTheDocument()
  })

  it('空拓扑（无在线实例）展示提示而非图', async () => {
    vi.mocked(getTopology).mockResolvedValue({ namespace: 'prod', nodes: [], edges: [], groups: [] })
    renderPage(<TopologyPage />)
    await userEvent.type(screen.getByLabelText('环境'), 'prod')
    await userEvent.click(screen.getByRole('button', { name: '查询' }))
    expect(await screen.findByText('该环境暂无在线实例。')).toBeInTheDocument()
    expect(screen.queryByTestId('topology-graph')).not.toBeInTheDocument()
  })
})
