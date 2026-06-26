// TopologyPage 单测（FR-37 + FR-105 真机打磨环境收口）：
// 覆盖「全局环境为全部时不查询/出提示 → 全局环境选具体环境后渲染拓扑图 → 切环境重查 → 喂图数据含真实节点/bc→bukkit 边/分组 → 空拓扑提示」。
// 环境改读页眉全局环境（不再页内自管下拉），故各用例用 setEnvironment 驱动。
// ECharts 依赖 canvas，故把 TopologyGraph 替身为轻量桩，断言喂图数据正确而不实际渲染。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
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
import { setEnvironment } from '@/state/environment'

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
  // 复位全局环境到「全部」（空串），由各用例按需切到具体环境
  setEnvironment('')
  vi.mocked(getTopology).mockReset()
  vi.mocked(getTopology).mockResolvedValue(TOPO)
})

describe('TopologyPage', () => {
  it('全局环境为「全部」时不查询、提示在页眉选具体环境', async () => {
    renderPage(<TopologyPage />)
    // 空全局环境（端点 namespace 必填）不发请求，并提示在页眉选具体环境
    expect(await screen.findByText(/请在页眉右上角「全局环境」选择具体环境/)).toBeInTheDocument()
    expect(vi.mocked(getTopology)).not.toHaveBeenCalled()
  })

  it('全局环境选具体环境后按其拉取拓扑并渲染图', async () => {
    setEnvironment('prod')
    renderPage(<TopologyPage />)
    await waitFor(() => expect(vi.mocked(getTopology)).toHaveBeenCalledWith('prod'))
    expect(await screen.findByTestId('topology-graph')).toBeInTheDocument()
  })

  it('切换全局环境后按该环境重查拓扑并渲染图', async () => {
    setEnvironment('prod')
    renderPage(<TopologyPage />)
    await screen.findByTestId('topology-graph')
    // 全局环境切到 test → 按 test 重查
    setEnvironment('test')
    await waitFor(() => expect(vi.mocked(getTopology)).toHaveBeenCalledWith('test'))
    expect(await screen.findByTestId('topology-graph')).toBeInTheDocument()
  })

  it('喂图数据含真实节点、bc→bukkit 边与大区/zone 分组', async () => {
    setEnvironment('prod')
    renderPage(<TopologyPage />)
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
    setEnvironment('prod')
    renderPage(<TopologyPage />)
    await screen.findByTestId('topology-graph')
    // 1 个 bc、2 个 bukkit
    expect(screen.getByText(/BC 代理（1）/)).toBeInTheDocument()
    expect(screen.getByText(/子服（2）/)).toBeInTheDocument()
  })

  it('空拓扑（无在线实例）展示提示而非图', async () => {
    setEnvironment('prod')
    vi.mocked(getTopology).mockResolvedValue({ namespace: 'prod', nodes: [], edges: [], groups: [] })
    renderPage(<TopologyPage />)
    expect(await screen.findByText('该环境暂无在线实例。')).toBeInTheDocument()
    expect(screen.queryByTestId('topology-graph')).not.toBeInTheDocument()
  })
})
