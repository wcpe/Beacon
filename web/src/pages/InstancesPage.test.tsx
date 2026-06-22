// InstancesPage 主动下线单测（FR-49）：锁定两项行为——
// ① 未在过滤条件选环境时也能按行下线，namespace 取自该行（不再强制先筛环境）；
// ② 下线调用携带该行的 serverId 与 namespace。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
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
  // FR-51：筛选框维度下拉的候选来源（环境 / zone 汇总）
  listNamespaces: vi.fn(),
  zoneSummary: vi.fn(),
}))

import InstancesPage from './InstancesPage'
import { listInstances, listOfflineInstances, offlineInstance, onlineInstance } from '../api/client'
import { listNamespaces, zoneSummary } from '../api/client'

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
    capacity: 0,
    weight: 0,
    metadata: {},
    lastHeartbeat: '',
    appliedMd5: '',
    playerCount: 0,
    tps: 0,
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

function renderPage(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

describe('InstancesPage 主动下线（FR-49）', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(listOfflineInstances).mockResolvedValue([])
    vi.mocked(offlineInstance).mockResolvedValue(undefined)
    vi.mocked(onlineInstance).mockResolvedValue(undefined)
    vi.mocked(listNamespaces).mockResolvedValue([{ code: 'prod', name: '生产' }])
    vi.mocked(zoneSummary).mockResolvedValue([])
  })

  it('未选环境也能按行下线，namespace 取自该行', async () => {
    vi.mocked(listInstances).mockResolvedValue([inst({ serverId: 'lobby-1', namespace: 'prod' })])
    const user = userEvent.setup()
    renderPage(<InstancesPage />)

    // 等行渲染（默认过滤为空，未指定环境）
    await waitFor(() => expect(screen.getByText('lobby-1')).toBeInTheDocument())

    // 点「下线」打开二次确认，再点「确认下线」
    await user.click(screen.getByRole('button', { name: '下线' }))
    await user.click(await screen.findByRole('button', { name: '确认下线' }))

    // 不应报「请先选环境」类错误；应以行 namespace 调用下线接口
    await waitFor(() =>
      expect(offlineInstance).toHaveBeenCalledWith('lobby-1', 'prod'),
    )
    expect(showError).not.toHaveBeenCalled()
  })

  it('已下线标记可取消下线，携带其 serverId 与 namespace', async () => {
    vi.mocked(listInstances).mockResolvedValue([])
    vi.mocked(listOfflineInstances).mockResolvedValue([
      { namespace: 'stage', serverId: 'gone-1', reason: '故障下架' },
    ])
    const user = userEvent.setup()
    renderPage(<InstancesPage />)

    await waitFor(() => expect(screen.getByText('gone-1')).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: '取消下线' }))

    await waitFor(() => expect(onlineInstance).toHaveBeenCalledWith('gone-1', 'stage'))
  })
})
