// SystemHeader 单测（FR-33）：
// 覆盖「连通态渲染各字段（含真实 CPU%）→ DB 断开反映为已断开 → 拉取失败反映为不可达 → CPU 不可用降级展示 → 采样器停用」。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'

// mock 后端调用，由各用例注入数据
vi.mock('@/api/client', async () => {
  const actual = await vi.importActual<typeof import('@/api/client')>('@/api/client')
  return {
    ApiClientError: actual.ApiClientError,
    systemStatus: vi.fn(),
    // FR-100 更新检查链路（useUpdateCheck）：默认无更新；红点用例各自注入
    checkUpdate: vi.fn(),
    listSettings: vi.fn().mockResolvedValue([]),
  }
})

// 监听跳转：版本徽章点击跳「版本与更新」页（ADR-0048，不再弹模态框）
const navigateSpy = vi.fn()
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom')
  return { ...actual, useNavigate: () => navigateSpy }
})

import SystemHeader from './SystemHeader'
import { systemStatus, checkUpdate } from '@/api/client'
import type { SystemStatusView, UpdateCheckView } from '@/api/types'

// 更新检查样例：有可用更新
const UPDATE_HAS: UpdateCheckView = {
  status: 'ok',
  currentVersion: 'v0.6.0',
  channel: 'stable',
  hasUpdate: true,
  isDevBuild: false,
  latestVersion: 'v0.7.0',
  releaseNotes: '变更',
  releaseUrl: 'https://github.com/wcpe/Beacon/releases/tag/v0.7.0',
  publishedAt: '2026-06-20T08:00:00Z',
  checkedAt: '2026-06-25T10:00:00Z',
  cacheExpiresAt: '2026-06-25T16:00:00Z',
}

// 健康样例：DB 连通、采样器启用、CPU 可用且占比 23.4%
const STATUS: SystemStatusView = {
  version: 'v0.6.0',
  startedAt: '2026-06-20T08:00:00Z',
  uptimeSeconds: 3 * 3600 + 25 * 60, // 3 小时 25 分
  db: { connected: true },
  onlineInstances: 7,
  samplerEnabled: true,
  runtime: {
    goroutines: 42,
    heapAlloc: 134217728, // 128 MB
    heapSys: 268435456, // 256 MB
  },
  cpuAvailable: true,
  cpuPercent: 23.4,
}

function renderHeader(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  // HeaderControls（FR-92）含 <Link>，需 Router 上下文
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  navigateSpy.mockReset()
  vi.mocked(systemStatus).mockResolvedValue(STATUS)
  // 默认无可用更新（无红点）；红点用例各自覆盖
  vi.mocked(checkUpdate).mockResolvedValue({ ...UPDATE_HAS, hasUpdate: false, latestVersion: '' })
})

describe('SystemHeader', () => {
  it('连通态渲染版本 / 运行时长 / 在线实例 / 采样器 / goroutine / 堆 / 真实 CPU%', async () => {
    renderHeader(<SystemHeader />)
    expect(await screen.findByText('v0.6.0')).toBeInTheDocument()
    expect(screen.getByText('控制面状态')).toBeInTheDocument()
    // DB 连通
    expect(screen.getByText('已连接')).toBeInTheDocument()
    // 运行时长取最高两个量级
    expect(screen.getByText('3 小时 25 分')).toBeInTheDocument()
    // 在线实例数
    expect(screen.getByText('7')).toBeInTheDocument()
    // 采样器启用
    expect(screen.getByText('已启用')).toBeInTheDocument()
    // goroutine 数
    expect(screen.getByText('42')).toBeInTheDocument()
    // Go 堆按字节格式化（used / sys）
    expect(screen.getByText('128 MB / 256 MB')).toBeInTheDocument()
    // 进程 CPU% 取真实采样值（保留 1 位小数）
    expect(screen.getByText('23.4%')).toBeInTheDocument()
  })

  it('CPU 不可用时降级展示「不可用」', async () => {
    vi.mocked(systemStatus).mockResolvedValue({ ...STATUS, cpuAvailable: false, cpuPercent: 0 })
    renderHeader(<SystemHeader />)
    expect(await screen.findByText('不可用')).toBeInTheDocument()
  })

  it('DB 断开时反映为「已断开」', async () => {
    vi.mocked(systemStatus).mockResolvedValue({
      ...STATUS,
      db: { connected: false, error: '连接已断开' },
    })
    renderHeader(<SystemHeader />)
    expect(await screen.findByText('已断开')).toBeInTheDocument()
    expect(screen.queryByText('已连接')).not.toBeInTheDocument()
  })

  it('拉取失败时反映为「不可达」', async () => {
    vi.mocked(systemStatus).mockRejectedValue(new Error('网络错误'))
    renderHeader(<SystemHeader />)
    expect(await screen.findByText('不可达')).toBeInTheDocument()
  })

  it('采样器停用时展示「已停用」', async () => {
    vi.mocked(systemStatus).mockResolvedValue({ ...STATUS, samplerEnabled: false })
    renderHeader(<SystemHeader />)
    expect(await screen.findByText('已停用')).toBeInTheDocument()
  })
})

// 版本徽章红点（FR-100）：hasUpdate 各分支显隐
describe('SystemHeader 更新红点（FR-100）', () => {
  it('hasUpdate=true 时版本徽章叠红点', async () => {
    vi.mocked(checkUpdate).mockResolvedValue(UPDATE_HAS)
    renderHeader(<SystemHeader />)
    expect(await screen.findByRole('status', { name: '有可用更新' })).toBeInTheDocument()
  })

  it('hasUpdate=false 时无红点', async () => {
    vi.mocked(checkUpdate).mockResolvedValue({ ...UPDATE_HAS, hasUpdate: false, latestVersion: '' })
    renderHeader(<SystemHeader />)
    // 版本徽章按钮先到位，确保更新检查已结算
    expect(await screen.findByRole('button', { name: /点击查看更新/ })).toBeInTheDocument()
    expect(screen.queryByRole('status', { name: '有可用更新' })).toBeNull()
  })

  it('check-failed 时不叠红点', async () => {
    vi.mocked(checkUpdate).mockResolvedValue({ ...UPDATE_HAS, status: 'check-failed', hasUpdate: false })
    renderHeader(<SystemHeader />)
    expect(await screen.findByRole('button', { name: /点击查看更新/ })).toBeInTheDocument()
    expect(screen.queryByRole('status', { name: '有可用更新' })).toBeNull()
  })

  it('dev 构建时不叠红点（即使后端误回 hasUpdate=true 也不提示）', async () => {
    vi.mocked(checkUpdate).mockResolvedValue({ ...UPDATE_HAS, isDevBuild: true, currentVersion: 'dev' })
    renderHeader(<SystemHeader />)
    expect(await screen.findByRole('button', { name: /点击查看更新/ })).toBeInTheDocument()
    expect(screen.queryByRole('status', { name: '有可用更新' })).toBeNull()
  })

  it('点击版本徽章跳转到版本与更新页（ADR-0048，不再弹模态框）', async () => {
    renderHeader(<SystemHeader />)
    const badge = await screen.findByRole('button', { name: /点击查看更新/ })
    await userEvent.click(badge)
    expect(navigateSpy).toHaveBeenCalledWith('/system/version')
  })
})
