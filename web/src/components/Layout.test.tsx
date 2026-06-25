// Layout 布局单测（FR-33 页眉重定位）：
// 锁定「品牌标题在侧边栏 → 控制面状态条收进右侧主内容列顶部（侧边栏之外、内容之上）」的结构关系。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { SystemStatusView } from '@/api/types'
import { currentPreferences, setNavExpandedGroups } from '@/state/preferences'

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
  // 偏好 store 快照为模块级、跨用例不重置；显式清空手动展开组，避免用例间串台。
  setNavExpandedGroups([])
  vi.mocked(systemStatus).mockResolvedValue(STATUS)
})

// 定位某分组的 details 容器（按组标题文本上溯到 details）
function findGroup(label: string): HTMLDetailsElement {
  const summary = screen.getByText(label)
  const details = summary.closest('details')
  if (!details) throw new Error(`找不到导航分组：${label}`)
  return details as HTMLDetailsElement
}

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

describe('Layout 侧栏导航手风琴（FR-93）', () => {
  it('渲染 5 组分组标题', () => {
    renderLayout()
    for (const label of ['概览', '配置管理', '集群', '可观测', '系统']) {
      expect(screen.getByText(label)).toBeInTheDocument()
    }
  })

  it('命中当前路由的组自动展开（其余组折叠）', () => {
    // 当前路由在 /servers → 集群组自动展开
    renderLayout('/servers')
    expect(findGroup('集群').open).toBe(true)
    // 概览组未命中、且无手动展开偏好 → 折叠
    expect(findGroup('概览').open).toBe(false)
    // 展开组内可见其叶子（服务器）
    const cluster = findGroup('集群')
    expect(within(cluster).getByText('服务器')).toBeInTheDocument()
  })

  it('手动展开某组写入偏好 navExpandedGroups（持久化）', async () => {
    const user = userEvent.setup()
    renderLayout('/servers')
    // 概览组初始折叠，点击其标题展开
    const overview = findGroup('概览')
    expect(overview.open).toBe(false)
    await user.click(within(overview).getByText('概览'))
    await waitFor(() => expect(currentPreferences().navExpandedGroups).toContain('overview'))
  })

  it('偏好里手动展开的组在非命中路由下也展开', () => {
    // 预置偏好：手动展开「可观测」组
    setNavExpandedGroups(['observability'])
    renderLayout('/dashboard')
    // 可观测组未命中路由，但因偏好手动展开仍打开
    expect(findGroup('可观测').open).toBe(true)
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
