// ProxiesPage 单测（FR-52，代理服管理页）：
// 覆盖「进页默认聚合全部环境拉 BC（不再要求先输入环境）→ 按环境筛选后按 role=bungee 重新拉取 +
// 拉默认入口 → 逐台展示状态/zone/连接数/线程/运行时长/后端可达·延迟/后端清单/所属小区默认入口 →
// 后端延迟 -1 显示『不可用』/无后端显示『无后端』 → 无 BC 空态」。

import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'

// mock 后端调用，由各用例注入数据
vi.mock('../api/client', () => ({
  listInstances: vi.fn(),
  listDefaultEntries: vi.fn(),
  // FR-51：环境筛选框下拉候选来源
  listNamespaces: vi.fn(),
}))

import ProxiesPage from './ProxiesPage'
import { listInstances, listDefaultEntries, listNamespaces } from '../api/client'
import type { InstanceView, DefaultEntryView } from '../api/types'

// 构造一台 BC（bungee）实例：默认填齐必填字段，overrides 覆盖关注项
function bc(overrides: Partial<InstanceView>): InstanceView {
  return {
    namespace: 'prod',
    serverId: 'bc-1',
    role: 'bungee',
    group: 'area1',
    zone: 'z1',
    assigned: true,
    address: '10.0.0.1:25577',
    version: '1.0',
    status: 'online',
    capacity: 0,
    weight: 0,
    metadata: {},
    lastHeartbeat: '',
    appliedMd5: '',
    playerCount: 0,
    tps: 0,
    backends: ['lobby-1', 'pvp-1'],
    zoneDefaultEntry: false,
    proxy: {
      onlineConnections: 312,
      threadCount: 48,
      uptimeMs: 3_600_000,
      backendUp: 2,
      backendTotal: 2,
      backendAvgLatencyMs: 12,
    },
    registeredAt: '',
    ...overrides,
  }
}

// 默认入口指向一个与后端清单不同的 serverId（entry-srv），便于断言不与后端 badge 混淆
const ENTRY: DefaultEntryView = {
  namespace: 'prod',
  group: 'area1',
  zone: 'z1',
  defaultServerId: 'entry-srv',
  updatedAt: '',
}

function renderPage(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.mocked(listInstances).mockReset()
  vi.mocked(listDefaultEntries).mockReset()
  vi.mocked(listNamespaces).mockReset()
  vi.mocked(listInstances).mockResolvedValue([bc({})])
  vi.mocked(listDefaultEntries).mockResolvedValue([ENTRY])
  vi.mocked(listNamespaces).mockResolvedValue([
    { code: 'prod', name: '生产' },
    { code: 'test', name: '测试' },
  ])
})

describe('ProxiesPage', () => {
  it('进页默认聚合全部环境拉取 BC（namespace 留空，无需先输入）', async () => {
    renderPage(<ProxiesPage />)
    // 进页即按 role=bungee 拉取，namespace 留空（聚合全部环境）
    await waitFor(() =>
      expect(vi.mocked(listInstances)).toHaveBeenCalledWith({ namespace: undefined, role: 'bungee' }),
    )
    expect(vi.mocked(listDefaultEntries)).toHaveBeenCalledWith(undefined)
    // 默认即展示出 BC 卡片，不再有「请先输入环境」提示
    expect(await screen.findByTestId('proxy-card-bc-1')).toBeInTheDocument()
  })

  it('按环境筛选后以该 namespace 重新拉取 BC 实例与默认入口', async () => {
    renderPage(<ProxiesPage />)
    // 环境筛选为可编辑 combobox：键入即生效，无需点查询按钮
    await userEvent.type(screen.getByLabelText('环境'), 'prod')
    await waitFor(() =>
      expect(vi.mocked(listInstances)).toHaveBeenCalledWith({ namespace: 'prod', role: 'bungee' }),
    )
    await waitFor(() => expect(vi.mocked(listDefaultEntries)).toHaveBeenCalledWith('prod'))
  })

  it('逐台展示底层参数与后端清单、所属小区默认入口', async () => {
    renderPage(<ProxiesPage />)
    const card = await screen.findByTestId('proxy-card-bc-1')
    const scoped = within(card)
    // 连接数 / 线程 / 后端可达性
    expect(scoped.getByText('312')).toBeInTheDocument()
    expect(scoped.getByText('48')).toBeInTheDocument()
    expect(scoped.getByText('2 / 2')).toBeInTheDocument()
    // 后端平均延迟
    expect(scoped.getByText('12 ms')).toBeInTheDocument()
    // 后端子服清单（FR-36）
    expect(scoped.getByText('lobby-1')).toBeInTheDocument()
    expect(scoped.getByText('pvp-1')).toBeInTheDocument()
    // 所属小区 z1 的默认入口（FR-48）：取自 default-entry，按 zone 索引
    expect(scoped.getByTestId('proxy-default-entry')).toHaveTextContent('entry-srv')
  })

  it('后端延迟 -1 显示不可用、无后端显示无后端', async () => {
    vi.mocked(listInstances).mockResolvedValue([
      bc({
        backends: [],
        proxy: {
          onlineConnections: 0,
          threadCount: 10,
          uptimeMs: 0,
          backendUp: 0,
          backendTotal: 0,
          backendAvgLatencyMs: -1,
        },
      }),
    ])
    renderPage(<ProxiesPage />)
    const card = await screen.findByTestId('proxy-card-bc-1')
    const scoped = within(card)
    expect(scoped.getByText('不可用')).toBeInTheDocument()
    // 「无后端」同时出现在「后端可达性」数值与「后端子服」空清单两处
    expect(scoped.getAllByText('无后端')).toHaveLength(2)
  })

  it('同环境下不同大区的同名 zone 各自显示正确默认入口（按 group+zone 复合键，不串）', async () => {
    // 两台 BC 分属不同大区但 zone 码同为 z1：area1/z1 与 area2/z1 是两个不同小区
    vi.mocked(listInstances).mockResolvedValue([
      bc({ serverId: 'bc-area1', group: 'area1', zone: 'z1' }),
      bc({ serverId: 'bc-area2', group: 'area2', zone: 'z1' }),
    ])
    // 默认入口按 (namespace, group, zone) 唯一：同名 zone 在两大区指向不同 serverId
    vi.mocked(listDefaultEntries).mockResolvedValue([
      { namespace: 'prod', group: 'area1', zone: 'z1', defaultServerId: 'entry-area1', updatedAt: '' },
      { namespace: 'prod', group: 'area2', zone: 'z1', defaultServerId: 'entry-area2', updatedAt: '' },
    ])
    renderPage(<ProxiesPage />)
    const card1 = await screen.findByTestId('proxy-card-bc-area1')
    const card2 = await screen.findByTestId('proxy-card-bc-area2')
    // 各大区卡片只显示自己大区同名 zone 的默认入口，不被另一大区覆盖
    expect(within(card1).getByTestId('proxy-default-entry')).toHaveTextContent('entry-area1')
    expect(within(card2).getByTestId('proxy-default-entry')).toHaveTextContent('entry-area2')
  })

  it('无 BC 实例时展示空态提示', async () => {
    vi.mocked(listInstances).mockResolvedValue([])
    renderPage(<ProxiesPage />)
    expect(await screen.findByText('暂无在线 BC 代理。')).toBeInTheDocument()
  })
})
