// Layout 布局单测（FR-33 页眉重定位）：
// 锁定「品牌标题在侧边栏 → 控制面状态条收进右侧主内容列顶部（侧边栏之外、内容之上）」的结构关系。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { SystemStatusView } from '@/api/types'

// mock 后端调用：SystemHeader 经 react-query 拉取自身状态 + FR-100 更新检查链路
vi.mock('@/api/client', () => ({
  systemStatus: vi.fn(),
  logout: vi.fn(),
  // FR-100：SystemHeader 的 useUpdateCheck 用到；默认无可用更新（无红点），不影响既有断言
  checkUpdate: vi.fn().mockResolvedValue({
    status: 'ok',
    currentVersion: 'v0.7.0',
    channel: 'stable',
    hasUpdate: false,
    isDevBuild: false,
    latestVersion: '',
    releaseNotes: '',
    releaseUrl: '',
    publishedAt: '',
    checkedAt: '',
    cacheExpiresAt: '',
  }),
  listSettings: vi.fn().mockResolvedValue([]),
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

function renderLayout(initialPath = '/') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[initialPath]}>
        <Layout />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  localStorage.clear()
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

describe('Layout 侧栏导航分组常驻（FR-93 方案 A）', () => {
  it('渲染 5 组分区标题', () => {
    renderLayout()
    for (const label of ['概览', '配置管理', '集群', '可观测', '系统']) {
      expect(screen.getByText(label)).toBeInTheDocument()
    }
  })

  it('叶子常驻显示（不折叠）：未命中路由的组其叶子也直接可见', () => {
    // 当前在 /servers，概览组未命中，但其叶子「可观测看板」仍常驻可见（无折叠）
    renderLayout('/servers')
    expect(screen.getByText('可观测看板')).toBeInTheDocument()
    expect(screen.getByText('服务器')).toBeInTheDocument()
    // 不再使用 details/summary 折叠容器
    expect(document.querySelector('aside details')).toBeNull()
  })

  it('每个叶子项前带 lucide 图标（size-4 svg）', () => {
    renderLayout('/servers')
    const link = screen.getByRole('link', { name: /服务器/ })
    const icon = link.querySelector('svg')
    expect(icon).not.toBeNull()
    expect(icon?.classList.contains('size-4')).toBe(true)
  })

  it('active 项精确单项高亮（end 精确匹配，不被前缀误高亮）', () => {
    // 在 /system 下，仅「控制面健康」(/system) 高亮，/system/version 不应被前缀误命中。
    // 用 classList 精确 token 判定：active 含独立 'bg-sidebar-accent'，
    // 非 active 仅含 'hover:bg-sidebar-accent/50'（不含独立 token）。
    renderLayout('/system')
    const sysHealth = screen.getByRole('link', { name: /控制面健康/ })
    const versionLink = screen.getByRole('link', { name: /版本与更新/ })
    expect(sysHealth.classList.contains('bg-sidebar-accent')).toBe(true)
    expect(versionLink.classList.contains('bg-sidebar-accent')).toBe(false)
    expect(versionLink.classList.contains('font-medium')).toBe(false)
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
