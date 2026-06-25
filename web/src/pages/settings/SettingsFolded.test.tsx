// 旧页折叠进设置子 tab + 旧路由重定向单测（FR-95，见 ADR-0043）：
// 锁定行为——
// ① 旧路由 /system、/api-keys、/namespaces 各前端 Navigate 重定向到对应设置子 tab 深链；
// ② 折叠后子 tab 渲染原页能力（控制面健康四组指标 / 密钥表 / 环境表），且不带重复的页级 <h1>；
// ③ 切走折叠了控制面健康页的子 tab 后，原页因 Radix Tabs 默认卸载而停轮询（systemObservability 不再被调）。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Routes, Route, Navigate, useLocation } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ObservabilityView, ApiKeyView, NamespaceView } from '../../api/types'

vi.mock('../../components/useMessage', () => ({
  useMessage: () => ({ showError: vi.fn(), showSuccess: vi.fn() }),
}))

// 折叠进子 tab 的三页各自的后端调用都 mock（由用例注入数据）。
// SystemConfigBlock 的网络代理 / 更新设置子 tab 走 listSettings（FR-101），一并 mock（本测试不激活那两 tab，置空即可）。
vi.mock('../../api/client', () => ({
  systemObservability: vi.fn(),
  listSettings: vi.fn().mockResolvedValue([]),
  updateSetting: vi.fn(),
  listApiKeys: vi.fn(),
  createApiKey: vi.fn(),
  resetApiKey: vi.fn(),
  revokeApiKey: vi.fn(),
  listNamespaces: vi.fn(),
  createNamespace: vi.fn(),
  updateNamespace: vi.fn(),
  deleteNamespace: vi.fn(),
}))

import SystemInfoBlock from './SystemInfoBlock'
import SystemConfigBlock from './SystemConfigBlock'
import {
  systemObservability,
  listSettings,
  listApiKeys,
  listNamespaces,
} from '../../api/client'

// jsdom 垫片：StatCard / Tabs 可能用到 scrollIntoView
if (!HTMLElement.prototype.scrollIntoView) {
  HTMLElement.prototype.scrollIntoView = () => {}
}

const OBS: ObservabilityView = {
  dbPool: { maxOpenConnections: 20, openConnections: 5, inUse: 12, idle: 13, waitCount: 7, waitDurationMs: 250 },
  longpoll: { config: 22, file: 11, topology: 0, command: 33, total: 66 },
  registryByStatus: { online: 41, degraded: 14, lost: 23 },
  registryTotal: 78,
  commandByStatus: { pending: 51, fetched: 17, done: 91 },
}

const KEYS: ApiKeyView[] = [
  {
    id: 1,
    name: '巡检密钥',
    role: 'readonly',
    keyPrefix: 'bk_abc',
    status: 'active',
    createdAt: '2026-06-25T00:00:00Z',
    lastUsedAt: null,
    expiresAt: null,
  },
]

const NS: NamespaceView[] = [
  { code: 'prod', name: '生产' },
  { code: 'test', name: '测试' },
]

// 落地路由把最终 location 写入 DOM（供断言重定向目标），并渲染目标块。
function LocationProbe({ block }: { block: React.ReactNode }) {
  const loc = useLocation()
  return (
    <>
      <div data-testid="loc">{loc.pathname + loc.search}</div>
      {block}
    </>
  )
}

// 旧路由重定向装配：镜像 App.tsx 的三条 Navigate。
function renderRedirects(path: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/system" element={<Navigate to="/settings/system-info?tab=health" replace />} />
          <Route
            path="/api-keys"
            element={<Navigate to="/settings/system-config?tab=api-keys" replace />}
          />
          <Route
            path="/namespaces"
            element={<Navigate to="/settings/system-config?tab=namespaces" replace />}
          />
          <Route
            path="/settings/system-info"
            element={<LocationProbe block={<SystemInfoBlock />} />}
          />
          <Route
            path="/settings/system-config"
            element={<LocationProbe block={<SystemConfigBlock />} />}
          />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

// 直接渲染某块（深链子 tab）。
function renderBlock(node: React.ReactNode, path: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/settings/system-info" element={node} />
          <Route path="/settings/system-config" element={node} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(systemObservability).mockResolvedValue(OBS)
  vi.mocked(listSettings).mockResolvedValue([])
  vi.mocked(listApiKeys).mockResolvedValue(KEYS)
  vi.mocked(listNamespaces).mockResolvedValue(NS)
})

describe('旧路由重定向到设置子 tab 深链（FR-95）', () => {
  it('/system 重定向到 /settings/system-info?tab=health（落控制面健康）', async () => {
    renderRedirects('/system')
    expect(await screen.findByTestId('loc')).toHaveTextContent('/settings/system-info?tab=health')
    // 控制面健康页四组指标之一可见，证明已落控制面健康子 tab
    expect(await screen.findByText('数据库连接池')).toBeInTheDocument()
  })

  it('/api-keys 重定向到 /settings/system-config?tab=api-keys（落密钥管理）', async () => {
    renderRedirects('/api-keys')
    expect(await screen.findByTestId('loc')).toHaveTextContent('/settings/system-config?tab=api-keys')
    expect(await screen.findByText('巡检密钥')).toBeInTheDocument()
  })

  it('/namespaces 重定向到 /settings/system-config?tab=namespaces（落环境管理）', async () => {
    renderRedirects('/namespaces')
    expect(await screen.findByTestId('loc')).toHaveTextContent(
      '/settings/system-config?tab=namespaces',
    )
    expect(await screen.findByText('prod')).toBeInTheDocument()
  })
})

describe('折叠子 tab 渲染原页能力且无重复页级 h1（FR-95）', () => {
  it('控制面健康子 tab 渲染四组指标', async () => {
    renderBlock(<SystemInfoBlock />, '/settings/system-info?tab=health')
    expect(await screen.findByText('数据库连接池')).toBeInTheDocument()
    expect(screen.getByText('命令队列深度')).toBeInTheDocument()
  })

  it('密钥管理子 tab 渲染密钥表 + 新建入口', async () => {
    renderBlock(<SystemConfigBlock />, '/settings/system-config?tab=api-keys')
    expect(await screen.findByText('巡检密钥')).toBeInTheDocument()
  })

  it('环境管理子 tab 渲染环境表', async () => {
    renderBlock(<SystemConfigBlock />, '/settings/system-config?tab=namespaces')
    expect(await screen.findByText('prod')).toBeInTheDocument()
    expect(screen.getByText('测试')).toBeInTheDocument()
  })

  it('折叠后无页级 <h1> 标题（标题由子 tab 标签承担）', async () => {
    renderBlock(<SystemInfoBlock />, '/settings/system-info?tab=health')
    await screen.findByText('数据库连接池')
    // 折叠页降级了内部 <h1>，块内不应再出现 h1 元素
    expect(document.querySelector('h1')).toBeNull()
  })
})

describe('切走折叠了控制面健康页的子 tab 停轮询（FR-95）', () => {
  it('切到版本与更新子 tab 后控制面健康页卸载、systemObservability 不再被调', async () => {
    renderBlock(<SystemInfoBlock />, '/settings/system-info?tab=health')
    // 先落控制面健康，确认已发起一次拉取
    await screen.findByText('数据库连接池')
    await waitFor(() => expect(vi.mocked(systemObservability)).toHaveBeenCalled())
    const callsBefore = vi.mocked(systemObservability).mock.calls.length

    // 切到「版本与更新」子 tab：Radix Tabs 默认卸载非激活 content → 控制面健康页卸载、轮询停
    const versionTab = screen.getByRole('tab', { name: '版本与更新' })
    await userEvent.click(versionTab)
    // 占位文案出现，证明已切走
    expect(await screen.findByText(/版本与更新信息即将在此呈现/)).toBeInTheDocument()
    // 控制面健康页已卸载，其四组指标不再在文档中
    await waitFor(() => expect(screen.queryByText('数据库连接池')).toBeNull())

    // 卸载后不应再有新的 systemObservability 调用（轮询已随卸载停止）
    const callsAfter = vi.mocked(systemObservability).mock.calls.length
    expect(callsAfter).toBe(callsBefore)
  })
})
