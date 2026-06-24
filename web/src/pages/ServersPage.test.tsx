// ServersPage 单测（FR-65，服务器页=实例与健康+代理服管理合并）：
// 锁定——① 列全部实例（bukkit+bungee，不限 role）；② 角色相关列（bukkit 人数·TPS / bungee 连接·运行时长·后端可达）；
// ③ 下线调 offlineInstance；④ drain 调 drainInstance；⑤ 改派触发 ReassignDialog；⑥ 未分配 zone 黄高亮。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'
import type { InstanceView } from '../api/types'

const showError = vi.fn()
const showSuccess = vi.fn()
vi.mock('../components/useMessage', () => ({
  useMessage: () => ({ showError, showSuccess }),
}))

vi.mock('../api/client', () => ({
  listInstances: vi.fn(),
  listOfflineInstances: vi.fn(),
  offlineInstance: vi.fn(),
  onlineInstance: vi.fn(),
  listDrains: vi.fn(),
  drainInstance: vi.fn(),
  undrainInstance: vi.fn(),
  triggerResync: vi.fn(),
  assignZone: vi.fn(),
  listAssignments: vi.fn(),
  listDefaultEntries: vi.fn(),
  listNamespaces: vi.fn(),
  zoneSummary: vi.fn(),
}))

import ServersPage from './ServersPage'
import {
  listInstances,
  listOfflineInstances,
  offlineInstance,
  onlineInstance,
  listDrains,
  drainInstance,
  undrainInstance,
  triggerResync,
  assignZone,
  listAssignments,
  listDefaultEntries,
  listNamespaces,
  zoneSummary,
} from '../api/client'

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
    status: 'online',
    capacity: 100,
    weight: 10,
    metadata: {},
    lastHeartbeat: '2026-06-20T08:00:00Z',
    lastHeartbeatAgeSec: 5,
    healthReason: '',
    appliedMd5: 'abc123',
    playerCount: 42,
    tps: 19.9,
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
    registeredAt: '2026-06-20T07:00:00Z',
    ...overrides,
  }
}

function bc(overrides: Partial<InstanceView>): InstanceView {
  return inst({
    serverId: 'proxy-1',
    role: 'bungee',
    address: '10.0.0.2:25577',
    playerCount: 0,
    tps: 0,
    backends: ['lobby-1', 'pvp-1'],
    proxy: {
      onlineConnections: 312,
      threadCount: 48,
      uptimeMs: 3_600_000,
      backendUp: 2,
      backendTotal: 2,
      backendAvgLatencyMs: 12,
    },
    ...overrides,
  })
}

function renderPage(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listInstances).mockResolvedValue([inst({}), bc({})])
  vi.mocked(listOfflineInstances).mockResolvedValue([])
  vi.mocked(offlineInstance).mockResolvedValue(undefined)
  vi.mocked(onlineInstance).mockResolvedValue(undefined)
  vi.mocked(listDrains).mockResolvedValue([])
  vi.mocked(drainInstance).mockResolvedValue({ namespace: 'prod', serverId: 'lobby-1', reason: '' })
  vi.mocked(undrainInstance).mockResolvedValue(undefined)
  vi.mocked(triggerResync).mockResolvedValue({ commandId: 1 })
  vi.mocked(assignZone).mockResolvedValue({
    namespace: 'prod',
    serverId: 'lobby-1',
    group: 'area1',
    zone: 'z2',
    note: '',
    updatedAt: '',
  })
  vi.mocked(listAssignments).mockResolvedValue([])
  vi.mocked(listDefaultEntries).mockResolvedValue([])
  vi.mocked(listNamespaces).mockResolvedValue([{ code: 'prod', name: '生产' }])
  vi.mocked(zoneSummary).mockResolvedValue([])
})

describe('ServersPage（FR-65 服务器页）', () => {
  it('统一列出全部实例（bukkit 与 bungee 均在表内，不限 role）', async () => {
    renderPage(<ServersPage />)
    expect(await screen.findByText('lobby-1')).toBeInTheDocument()
    expect(screen.getByText('proxy-1')).toBeInTheDocument()
    // 进页默认按空过滤拉全部实例（不带 role 限制）
    await waitFor(() => expect(vi.mocked(listInstances)).toHaveBeenCalled())
    const firstCall = vi.mocked(listInstances).mock.calls[0][0]
    expect(firstCall).not.toHaveProperty('role')
  })

  it('角色相关列：bukkit 显人数·TPS，bungee 显连接·后端可达', async () => {
    renderPage(<ServersPage />)
    const row = (await screen.findByText('lobby-1')).closest('tr')!
    // bukkit 行显示人数 42 与 TPS 19.9
    expect(within(row).getByText('42')).toBeInTheDocument()
    expect(within(row).getByText('19.9')).toBeInTheDocument()
    // bungee 行显示在线连接 312 与后端可达 2 / 2
    const bcRow = screen.getByText('proxy-1').closest('tr')!
    expect(within(bcRow).getByText('312')).toBeInTheDocument()
    expect(within(bcRow).getByText('2 / 2')).toBeInTheDocument()
  })

  it('未分配 zone 的行黄色高亮 + 未分配徽标', async () => {
    vi.mocked(listInstances).mockResolvedValue([inst({ serverId: 'free-1', zone: null, assigned: false })])
    renderPage(<ServersPage />)
    const row = (await screen.findByText('free-1')).closest('tr')!
    expect(row.className).toContain('bg-amber-50')
    expect(within(row).getByText('未分配')).toBeInTheDocument()
  })

  it('按行下线：菜单内点下线 → 二次确认后调 offlineInstance（namespace 取自该行）', async () => {
    vi.mocked(listInstances).mockResolvedValue([inst({ serverId: 'lobby-1', namespace: 'prod' })])
    const user = userEvent.setup()
    renderPage(<ServersPage />)
    await waitFor(() => expect(screen.getByText('lobby-1')).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: '行操作' }))
    await user.click(await screen.findByRole('menuitem', { name: '下线' }))
    // 确认弹窗在菜单外层受控触发，绝不丢二次确认
    await user.click(await screen.findByRole('button', { name: '确认下线' }))
    await waitFor(() => expect(offlineInstance).toHaveBeenCalledWith('lobby-1', 'prod'))
    expect(showError).not.toHaveBeenCalled()
  })

  it('按行排空：菜单内点 drain 调 drainInstance（携带该行 serverId 与 namespace）', async () => {
    vi.mocked(listInstances).mockResolvedValue([inst({ serverId: 'lobby-1', namespace: 'prod' })])
    const user = userEvent.setup()
    renderPage(<ServersPage />)
    await waitFor(() => expect(screen.getByText('lobby-1')).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: '行操作' }))
    await user.click(await screen.findByRole('menuitem', { name: '排空' }))
    await waitFor(() => expect(drainInstance).toHaveBeenCalledWith('lobby-1', 'prod'))
  })

  it('已排空实例菜单内显示「取消排空」并调 undrainInstance', async () => {
    vi.mocked(listInstances).mockResolvedValue([inst({ serverId: 'lobby-1', namespace: 'prod' })])
    vi.mocked(listDrains).mockResolvedValue([{ namespace: 'prod', serverId: 'lobby-1', reason: '维护' }])
    const user = userEvent.setup()
    renderPage(<ServersPage />)
    await waitFor(() => expect(screen.getByText('lobby-1')).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: '行操作' }))
    await user.click(await screen.findByRole('menuitem', { name: '取消排空' }))
    await waitFor(() => expect(undrainInstance).toHaveBeenCalledWith('lobby-1', 'prod'))
  })

  it('菜单内点「改派」打开改派对话框（ReassignDialog）', async () => {
    vi.mocked(listInstances).mockResolvedValue([inst({ serverId: 'lobby-1' })])
    const user = userEvent.setup()
    renderPage(<ServersPage />)
    await waitFor(() => expect(screen.getByText('lobby-1')).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: '行操作' }))
    await user.click(await screen.findByRole('menuitem', { name: '改派' }))
    // ReassignDialog 标题含被改派 serverId
    expect(await screen.findByText('改派 lobby-1')).toBeInTheDocument()
  })

  it('行操作菜单含三新项（agent 详情 / 查看日志 / 强制重同步）', async () => {
    vi.mocked(listInstances).mockResolvedValue([inst({ serverId: 'lobby-1' })])
    const user = userEvent.setup()
    renderPage(<ServersPage />)
    await waitFor(() => expect(screen.getByText('lobby-1')).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: '行操作' }))
    expect(await screen.findByRole('menuitem', { name: 'agent 详情' })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: '查看日志' })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: '强制重同步' })).toBeInTheDocument()
  })

  it('菜单内点「强制重同步」调 triggerResync 并提示成功', async () => {
    vi.mocked(listInstances).mockResolvedValue([inst({ serverId: 'lobby-1', namespace: 'prod' })])
    const user = userEvent.setup()
    renderPage(<ServersPage />)
    await waitFor(() => expect(screen.getByText('lobby-1')).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: '行操作' }))
    await user.click(await screen.findByRole('menuitem', { name: '强制重同步' }))
    await waitFor(() => expect(triggerResync).toHaveBeenCalledWith('lobby-1', 'prod'))
    await waitFor(() => expect(showSuccess).toHaveBeenCalled())
  })

  it('已主动下线区可取消下线，携带其 serverId 与 namespace', async () => {
    vi.mocked(listInstances).mockResolvedValue([])
    vi.mocked(listOfflineInstances).mockResolvedValue([
      { namespace: 'stage', serverId: 'gone-1', reason: '故障下架' },
    ])
    const user = userEvent.setup()
    renderPage(<ServersPage />)
    await waitFor(() => expect(screen.getByText('gone-1')).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: '取消下线' }))
    await waitFor(() => expect(onlineInstance).toHaveBeenCalledWith('gone-1', 'stage'))
  })
})
