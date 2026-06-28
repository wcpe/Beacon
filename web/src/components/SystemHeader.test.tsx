// SystemHeader 单测（FR-33 / FR-121，精简版）：
// 覆盖「连通态药丸 + 运行/在线合并 → DB 断开反映为已断开 → 拉取失败反映为不可达 →
// 不再渲染采样器/goroutine/堆/CPU（已迁控制面健康页）→ 首次加载显骨架 →
// 搜索入口（FR-83）→ 右上角账户菜单（FR-121）」。
// 版本徽章（FR-100）已移至整宽顶栏品牌区独立组件，对应用例迁至 VersionBadge.test。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
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
    // VersionBadge（FR-126：版本徽章移到状态条「已连接」后）用 useUpdateCheck → checkUpdate；默认无更新
    checkUpdate: vi.fn().mockResolvedValue({
      status: 'ok',
      currentVersion: 'v0.6.0',
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
    // VersionBadge 的 useUpdateCheck 还读 listSettings（取渠道）；默认空
    listSettings: vi.fn().mockResolvedValue([]),
    // OperatorMenu（FR-121 右上角账户菜单）导入 logout；渲染不调用，仅登出点击触发
    logout: vi.fn(),
  }
})

// 监听跳转：账户菜单登出跳 /login（OperatorMenu）
const navigateSpy = vi.fn()
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom')
  return { ...actual, useNavigate: () => navigateSpy }
})

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
  // HeaderControls（FR-92）含 <Link>、OperatorMenu（FR-121）含 useNavigate，需 Router 上下文
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  navigateSpy.mockReset()
  vi.mocked(systemStatus).mockResolvedValue(STATUS)
})

describe('SystemHeader', () => {
  it('连通态渲染已连接药丸 + 运行/在线合并行', async () => {
    renderHeader(<SystemHeader />)
    expect(await screen.findByText('已连接')).toBeInTheDocument()
    expect(screen.getByText('控制面状态')).toBeInTheDocument()
    // 运行/在线合并为一行：运行 X · 在线 N
    expect(screen.getByText('运行 3 小时 25 分 · 在线 7')).toBeInTheDocument()
  })

  it('运行/在线只一行：不再渲染「运行 / 在线」标签行（FR-118 E）', async () => {
    renderHeader(<SystemHeader />)
    expect(await screen.findByText('运行 3 小时 25 分 · 在线 7')).toBeInTheDocument()
    expect(screen.queryByText('运行 / 在线')).toBeNull()
  })

  it('不再渲染采样器 / goroutine / Go 堆 / 进程 CPU%（已迁控制面健康页）', async () => {
    renderHeader(<SystemHeader />)
    await screen.findByText('已连接')
    expect(screen.queryByText('已启用')).toBeNull()
    expect(screen.queryByText('42')).toBeNull()
    expect(screen.queryByText('128 MB / 256 MB')).toBeNull()
    expect(screen.queryByText('23.4%')).toBeNull()
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

  it('首次加载时连接态 / 运行行显骨架（不闪 -）', async () => {
    vi.mocked(systemStatus).mockReturnValue(new Promise(() => {}))
    const { container } = renderHeader(<SystemHeader />)
    expect(container.querySelectorAll('.animate-pulse').length).toBeGreaterThan(0)
    expect(screen.queryByText('已连接')).toBeNull()
    expect(screen.queryByText('已断开')).toBeNull()
  })
})

// 右上角搜索入口（FR-83）：由侧栏移至页眉，点击调 onOpenSearch 打开命令面板
describe('SystemHeader 搜索入口（FR-83）', () => {
  it('渲染右上角搜索触发（含「搜索…」与 Ctrl K 提示）', async () => {
    renderHeader(<SystemHeader onOpenSearch={() => {}} />)
    const trigger = await screen.findByRole('button', { name: '搜索…' })
    expect(trigger).toBeInTheDocument()
    expect(within(trigger).getByText('Ctrl K')).toBeInTheDocument()
  })

  it('点击搜索触发调用 onOpenSearch', async () => {
    const onOpenSearch = vi.fn()
    renderHeader(<SystemHeader onOpenSearch={onOpenSearch} />)
    await userEvent.click(await screen.findByRole('button', { name: '搜索…' }))
    expect(onOpenSearch).toHaveBeenCalledTimes(1)
  })
})

// 右上角账户菜单（FR-121）：操作人 + 登出从侧栏底部移来，呈首字母头像
describe('SystemHeader 账户菜单（FR-121）', () => {
  it('右上角渲染账户菜单头像', async () => {
    renderHeader(<SystemHeader />)
    expect(await screen.findByRole('button', { name: '账户菜单' })).toBeInTheDocument()
  })
})

// 版本徽章（FR-126）：从品牌区移到状态条「已连接」之后，可点进版本页
describe('SystemHeader 版本徽章（FR-126）', () => {
  it('状态条内渲染版本徽章', async () => {
    renderHeader(<SystemHeader />)
    expect(await screen.findByText('v0.6.0')).toBeInTheDocument()
  })
})

// 窄屏渐进收窄（fix-A 回归护栏）：顶栏固定高 h-10 + 原 flex-wrap 致窄屏换行被裁。
// jsdom 无布局引擎、无法测真实溢出，仅锁结构类名作护栏；真实收窄行为由浏览器验证。
describe('SystemHeader 窄屏渐进收窄（fix-A）', () => {
  it('外层不换行（flex-nowrap 而非 flex-wrap），避免固定高顶栏内换行被裁', async () => {
    renderHeader(<SystemHeader />)
    const title = await screen.findByText('控制面状态')
    const root = title.parentElement as HTMLElement
    expect(root.classList.contains('flex-nowrap')).toBe(true)
    expect(root.classList.contains('flex-wrap')).toBe(false)
  })

  it('次要项（标题 / 运行·在线）带响应式隐藏断点类，窄屏收起', async () => {
    renderHeader(<SystemHeader />)
    // 标题：窄屏隐藏（hidden + 某断点显示）
    const title = screen.getByText('控制面状态')
    expect(title.className).toMatch(/hidden/)
    expect(title.className).toMatch(/(md|lg|xl):(inline|block|flex)/)
    // 运行 / 在线：窄屏隐藏
    const runtime = await screen.findByText('运行 3 小时 25 分 · 在线 7')
    expect(runtime.className).toMatch(/hidden/)
    expect(runtime.className).toMatch(/(md|lg|xl):(inline|block|flex)/)
  })
})
