// Layout 布局单测（FR-33 页眉重定位）：
// 锁定「品牌标题在侧边栏 → 控制面状态条收进右侧主内容列顶部（侧边栏之外、内容之上）」的结构关系。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { SystemStatusView } from '@/api/types'

// mock 后端调用：SystemHeader 经 react-query 拉取自身状态
vi.mock('@/api/client', () => ({
  systemStatus: vi.fn(),
  logout: vi.fn(),
}))

import Layout from './Layout'
import { systemStatus } from '@/api/client'

// 健康样例：仅供 SystemHeader 渲染，断言不依赖具体数值
const STATUS: SystemStatusView = {
  version: 'v0.7.0',
  startedAt: '2026-06-20T08:00:00Z',
  uptimeSeconds: 60,
  db: { connected: true },
  onlineInstances: 1,
  samplerEnabled: true,
  runtime: { goroutines: 1, heapAlloc: 1024, heapSys: 2048 },
  cpuAvailable: true,
  cpuPercent: 1.2,
}

function renderLayout() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <Layout />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.mocked(systemStatus).mockResolvedValue(STATUS)
})

describe('Layout 页眉重定位', () => {
  it('品牌标题渲染在侧边栏内', () => {
    renderLayout()
    const brand = screen.getByText('Beacon 管理台')
    // 品牌标题应位于侧边栏（aside）内
    expect(brand.closest('aside')).not.toBeNull()
  })

  it('控制面状态条收进右侧主内容列顶部（不在侧边栏内、位于内容之上）', async () => {
    renderLayout()
    const stateLabel = await screen.findByText('控制面状态')
    // 状态条不应落在侧边栏内
    expect(stateLabel.closest('aside')).toBeNull()
    // 状态条容器是 header，且与 <main> 同处一个 flex 列（共享同一父节点）
    const headerEl = stateLabel.closest('header')
    expect(headerEl).not.toBeNull()
    const mainEl = document.querySelector('main')
    expect(mainEl).not.toBeNull()
    expect(headerEl?.parentElement).toBe(mainEl?.parentElement)
  })

  it('主内容区纵向可滚动（不裁剪超高内容，回归「滚动锁死」）', () => {
    renderLayout()
    // <main> 须为 overflow-y-auto 而非 overflow-hidden：
    // 普通堆叠页内容超过视口高度时应可滚动看全，不被裁在左上角。
    const mainEl = document.querySelector('main')
    expect(mainEl).not.toBeNull()
    expect(mainEl?.classList.contains('overflow-y-auto')).toBe(true)
    expect(mainEl?.classList.contains('overflow-hidden')).toBe(false)
  })
})

describe('Layout 连接状态指示（FR-78）', () => {
  it('连通时不弹断开横幅', async () => {
    vi.mocked(systemStatus).mockResolvedValue(STATUS)
    renderLayout()
    // 等心跳查询成功后，确认无断开横幅
    await screen.findByText('控制面状态')
    await waitFor(() =>
      expect(screen.queryByText('控制面连接中断，正在重连…')).not.toBeInTheDocument(),
    )
  })

  it('心跳查询失败时弹「控制面连接中断，正在重连…」断开横幅', async () => {
    vi.mocked(systemStatus).mockRejectedValue(new Error('网络断开'))
    renderLayout()
    expect(await screen.findByText('控制面连接中断，正在重连…')).toBeInTheDocument()
  })
})
