// SystemHeader 单测（FR-33）：
// 覆盖「连通态渲染各字段（含真实 CPU%）→ DB 断开反映为已断开 → 拉取失败反映为不可达 → CPU 不可用降级展示 → 采样器停用」。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'

// mock 后端调用，由各用例注入数据
vi.mock('@/api/client', () => ({
  systemStatus: vi.fn(),
}))

import SystemHeader from './SystemHeader'
import { systemStatus } from '@/api/client'
import type { SystemStatusView } from '@/api/types'

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
  vi.mocked(systemStatus).mockResolvedValue(STATUS)
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
