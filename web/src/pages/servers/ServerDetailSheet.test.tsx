// ServerDetailSheet 变更历史区测试（FR-80）：
// Sheet 打开时按 serverId 拉有效配置变更时间线，列出每条（dataId · scope · 版本 · 时间 · 操作者）；空集给提示。
// serverConfigTimeline 被 mock 为可控数据，保证用例在 jsdom 下稳定可跑。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'

import type { ConfigTimelineView, InstanceView } from '../../api/types'

// mock 时间线端点（FR-80），由各用例注入数据
vi.mock('../../api/client', () => ({
  serverConfigTimeline: vi.fn(),
}))

import ServerDetailSheet from './ServerDetailSheet'
import { serverConfigTimeline } from '../../api/client'

// 最小 bukkit 实例（仅本测试关心的字段，其余给零值）
function inst(overrides: Partial<InstanceView> = {}): InstanceView {
  return {
    namespace: 'prod',
    serverId: 'lobby-1',
    role: 'bukkit',
    group: 'area1',
    zone: 'zoneA',
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
    registeredAt: '2026-06-20T07:00:00Z',
    ...overrides,
  }
}

function renderSheet(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.mocked(serverConfigTimeline).mockReset()
})

describe('ServerDetailSheet 变更历史（FR-80）', () => {
  it('列出时间线条目：dataId / 覆盖层 / 版本 / 操作者', async () => {
    vi.mocked(serverConfigTimeline).mockResolvedValue({
      namespace: 'prod',
      serverId: 'lobby-1',
      group: 'area1',
      zone: 'zoneA',
      items: [
        {
          configItemId: 1,
          dataId: 'mysql.yml',
          scopeLevel: 'global',
          scopeTarget: '',
          version: 2,
          md5: 'aaa',
          operator: 'alice',
          comment: '调大池',
          createdAt: '2026-06-20T10:00:00Z',
        },
        {
          configItemId: 2,
          dataId: 'mysql.yml',
          scopeLevel: 'server',
          scopeTarget: 'lobby-1',
          version: 1,
          md5: 'bbb',
          operator: 'bob',
          comment: '',
          createdAt: '2026-06-20T09:00:00Z',
        },
      ],
    } satisfies ConfigTimelineView)

    renderSheet(<ServerDetailSheet instance={inst()} onOpenChange={() => {}} />)

    expect(await screen.findByText('变更历史')).toBeInTheDocument()
    // 两条 mysql.yml（不同层）
    expect(await screen.findAllByText('mysql.yml')).toHaveLength(2)
    // 覆盖层中文标签
    expect(screen.getByText('全局层')).toBeInTheDocument()
    expect(screen.getByText('子服层')).toBeInTheDocument()
    // 版本徽标
    expect(screen.getByText('v2')).toBeInTheDocument()
    expect(screen.getByText('v1')).toBeInTheDocument()
    // 操作者
    expect(screen.getByText('alice')).toBeInTheDocument()
    expect(screen.getByText('bob')).toBeInTheDocument()
    // 端点按 serverId + namespace 调用
    expect(serverConfigTimeline).toHaveBeenCalledWith({
      serverId: 'lobby-1',
      namespace: 'prod',
      group: 'area1',
    })
  })

  it('时间线为空时给「暂无配置变更记录」提示', async () => {
    vi.mocked(serverConfigTimeline).mockResolvedValue({
      namespace: 'prod',
      serverId: 'lobby-1',
      group: 'area1',
      zone: 'zoneA',
      items: [],
    } satisfies ConfigTimelineView)

    renderSheet(<ServerDetailSheet instance={inst()} onOpenChange={() => {}} />)

    expect(await screen.findByText('该服覆盖链暂无配置变更记录')).toBeInTheDocument()
  })
})
