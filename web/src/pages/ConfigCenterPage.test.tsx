// 配置中心 FR-137 最小视觉重排单测：锁定三栏、薄工具栏、右侧队列入口与发布确认门。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { InstanceView } from '@/api/types'

vi.mock('@monaco-editor/react', () => ({
  __esModule: true,
  default: () => <div data-testid="monaco-editor" />,
  DiffEditor: () => <div data-testid="monaco-diff" />,
}))

vi.mock('@/api/client', () => ({
  listInstances: vi.fn(),
}))

import ConfigCenterPage from './ConfigCenterPage'
import { listInstances } from '@/api/client'

const ZERO_PROXY: InstanceView['proxy'] = {
  onlineConnections: 0,
  threadCount: 0,
  uptimeMs: 0,
  backendUp: 0,
  backendTotal: 0,
  backendAvgLatencyMs: -1,
}

function instance(partial: Partial<InstanceView> & Pick<InstanceView, 'serverId'>): InstanceView {
  return {
    namespace: 'prod',
    serverId: partial.serverId,
    role: 'bukkit',
    group: partial.group ?? 'server-a',
    zone: partial.zone ?? 'zone-01',
    assigned: true,
    address: '10.0.0.1:25565',
    version: '1.20.4',
    agentVersion: '0.12.0',
    status: partial.status ?? 'online',
    capacity: 100,
    weight: 1,
    metadata: {},
    lastHeartbeat: '2026-07-05T00:00:00Z',
    lastHeartbeatAgeSec: 1,
    healthReason: '',
    appliedMd5: 'abc',
    playerCount: 0,
    tps: 20,
    backends: [],
    zoneDefaultEntry: false,
    proxy: ZERO_PROXY,
    registeredAt: '2026-07-05T00:00:00Z',
    ...partial,
  }
}

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <ConfigCenterPage />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listInstances).mockResolvedValue([
    instance({ serverId: 'server-01' }),
    instance({ serverId: 'server-02', zone: 'zone-02' }),
  ])
})

describe('ConfigCenterPage FR-137 视觉重排', () => {
  it('保留薄工具栏与左中右三栏', async () => {
    renderPage()

    const toolbar = screen.getByRole('toolbar', { name: '配置上下文工具栏' })
    expect(within(toolbar).getByText('看生效')).toBeInTheDocument()
    expect(within(toolbar).getByText('写入层')).toBeInTheDocument()
    expect(within(toolbar).getByText('影响范围')).toBeInTheDocument()
    expect(within(toolbar).getByText('server-01')).toBeInTheDocument()

    expect(screen.getByRole('region', { name: '资源' })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: '工作区' })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: '上下文 / 队列' })).toBeInTheDocument()
  })

  it('模式切换仍可在编辑、层列、对盘之间切换', async () => {
    renderPage()

    await userEvent.click(screen.getByRole('button', { name: '层列' }))
    expect(screen.getByText(/键路径（折叠树）/)).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: '对盘' }))
    expect(screen.getByText(/中心生效/)).toBeInTheDocument()
  })

  it('右侧上下文区域承接队列与操作日志入口', async () => {
    renderPage()

    const rightRail = screen.getByRole('region', { name: '上下文 / 队列' })
    expect(within(rightRail).getByText('队列 / 操作日志')).toBeInTheDocument()
    expect(within(rightRail).getByRole('button', { name: /抓取 \/ 收编/ })).toBeInTheDocument()
    await userEvent.click(within(rightRail).getByRole('button', { name: /发布 \/ 灰度/ }))
    expect(within(rightRail).getByText(/灰度 paper-global.yml/)).toBeInTheDocument()
  })

  it('发布入口仍打开二次确认门且默认禁用确认按钮', async () => {
    renderPage()

    await userEvent.click(screen.getByRole('button', { name: '发布' }))

    expect(screen.getByText('发布 · config.yml')).toBeInTheDocument()
    expect(screen.getByText(/我确认将 config.yml 发布到/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '发布并热更' })).toBeDisabled()
  })
})
