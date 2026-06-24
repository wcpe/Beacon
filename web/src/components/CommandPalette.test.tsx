// 命令面板组件单测（FR-83）：锁定唤起渲染 / 即时过滤 / 分组 / 键盘上下回车跳转 / 数据源失败降级。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ConfigView, FileView, InstanceView } from '@/api/types'

// mock 列表端点：组件打开时拉配置 / 文件 / 实例
vi.mock('@/api/client', () => ({
  listConfigs: vi.fn(),
  listFiles: vi.fn(),
  listInstances: vi.fn(),
}))

// 监听跳转：用一个落点组件回显当前路径
const navigateSpy = vi.fn()
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom')
  return { ...actual, useNavigate: () => navigateSpy }
})

import CommandPalette from './CommandPalette'
import { listConfigs, listFiles, listInstances } from '@/api/client'

const CONFIG: ConfigView = {
  id: 1,
  namespace: 'prod',
  group: 'g1',
  dataId: 'server.yml',
  scopeLevel: 'group',
  scopeTarget: 'g1',
  format: 'yaml',
  version: 1,
  md5: 'x',
  enabled: true,
  updatedAt: '',
}

const FILE: FileView = {
  id: 2,
  namespace: 'prod',
  group: 'g1',
  path: 'plugins/Foo/config.yml',
  scopeLevel: 'group',
  scopeTarget: 'g1',
  version: 1,
  md5: 'x',
  enabled: true,
  updatedAt: '',
}

const INSTANCE: InstanceView = {
  namespace: 'prod',
  serverId: 'lobby-1',
  role: 'bukkit',
  group: 'g1',
  zone: null,
  assigned: false,
  address: '',
  version: '',
  status: 'online',
  capacity: 0,
  weight: 0,
  metadata: {},
  lastHeartbeat: '',
  lastHeartbeatAgeSec: 0,
  healthReason: '',
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
}

function renderPalette(open = true) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <CommandPalette open={open} onOpenChange={() => {}} />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  navigateSpy.mockReset()
  vi.mocked(listConfigs).mockResolvedValue([CONFIG])
  vi.mocked(listFiles).mockResolvedValue([FILE])
  vi.mocked(listInstances).mockResolvedValue([INSTANCE])
})

describe('CommandPalette 全局命令面板（FR-83）', () => {
  it('打开时渲染搜索框与导航分组', async () => {
    renderPalette()
    expect(screen.getByPlaceholderText('搜索页面 / 配置 / 服务器 / 审计动作…')).toBeInTheDocument()
    // 默认（空 query）展示导航与审计动作分组
    expect(await screen.findByText('导航')).toBeInTheDocument()
    expect(screen.getByRole('option', { name: /配置中心/ })).toBeInTheDocument()
  })

  it('输入关键字即时过滤并按命中数据分组（配置 / 服务器）', async () => {
    const user = userEvent.setup()
    renderPalette()
    // 等数据拉取完成（配置项出现需 query 命中）
    await screen.findByText('导航')
    await user.type(screen.getByPlaceholderText('搜索页面 / 配置 / 服务器 / 审计动作…'), 'server')
    // 命中配置 server.yml（配置 / 文件分组）
    await waitFor(() => expect(screen.getByText('server.yml')).toBeInTheDocument())
    expect(screen.getByText('配置 / 文件')).toBeInTheDocument()
  })

  it('命中服务器项回车跳转到 /servers?serverId=', async () => {
    const user = userEvent.setup()
    renderPalette()
    await screen.findByText('导航')
    await user.type(screen.getByPlaceholderText('搜索页面 / 配置 / 服务器 / 审计动作…'), 'lobby')
    await waitFor(() => expect(screen.getByText('lobby-1')).toBeInTheDocument())
    // 唯一命中即首选，回车直接跳
    await user.keyboard('{Enter}')
    expect(navigateSpy).toHaveBeenCalledWith('/servers?serverId=lobby-1')
  })

  it('方向键下移改变选中并回车跳到第二项', async () => {
    const user = userEvent.setup()
    renderPalette()
    await screen.findByText('导航')
    // 空 query 下导航首项为「可观测看板」(/dashboard)，下移一格到「配置中心」(/configs)
    await user.keyboard('{ArrowDown}')
    await user.keyboard('{Enter}')
    expect(navigateSpy).toHaveBeenCalledWith('/configs')
  })

  it('点击结果项跳转', async () => {
    const user = userEvent.setup()
    renderPalette()
    const opt = await screen.findByRole('option', { name: /可观测看板/ })
    await user.click(opt)
    expect(navigateSpy).toHaveBeenCalledWith('/dashboard')
  })

  it('数据源失败时仍渲染导航 + 审计动作（降级不阻断）', async () => {
    vi.mocked(listConfigs).mockRejectedValue(new Error('后端不可用'))
    vi.mocked(listFiles).mockRejectedValue(new Error('后端不可用'))
    vi.mocked(listInstances).mockRejectedValue(new Error('后端不可用'))
    renderPalette()
    // 导航与审计动作为静态项，不依赖后端
    expect(await screen.findByText('导航')).toBeInTheDocument()
    expect(screen.getByText('审计动作')).toBeInTheDocument()
  })

  it('无命中显示空态', async () => {
    const user = userEvent.setup()
    renderPalette()
    await screen.findByText('导航')
    await user.type(
      screen.getByPlaceholderText('搜索页面 / 配置 / 服务器 / 审计动作…'),
      '绝不存在zzz',
    )
    expect(await screen.findByText('无匹配结果')).toBeInTheDocument()
  })
})
