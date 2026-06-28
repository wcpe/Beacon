// VersionBadge 单测（FR-100 / FR-121）：版本号显示 + 点击跳「版本与更新」页 + 有更新叠红点。
// 从原 SystemHeader 版本徽章用例迁来（FR-121 徽章移至品牌区独立组件）。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

vi.mock('@/api/client', async () => {
  const actual = await vi.importActual<typeof import('@/api/client')>('@/api/client')
  return {
    ApiClientError: actual.ApiClientError,
    systemStatus: vi.fn(),
    checkUpdate: vi.fn(),
    listSettings: vi.fn().mockResolvedValue([]),
  }
})

// 监听跳转：版本徽章点击跳「版本与更新」页（ADR-0048）
const navigateSpy = vi.fn()
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom')
  return { ...actual, useNavigate: () => navigateSpy }
})

import VersionBadge from './VersionBadge'
import { systemStatus, checkUpdate } from '@/api/client'
import type { SystemStatusView, UpdateCheckView } from '@/api/types'

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

const STATUS: SystemStatusView = {
  version: 'v0.6.0',
  startedAt: '2026-06-20T08:00:00Z',
  uptimeSeconds: 60,
  db: { connected: true },
  onlineInstances: 1,
  samplerEnabled: true,
  runtime: { goroutines: 1, heapAlloc: 1024, heapSys: 2048 },
  cpuAvailable: true,
  cpuPercent: 1.2,
}

function renderBadge() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <VersionBadge />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  navigateSpy.mockReset()
  vi.mocked(systemStatus).mockResolvedValue(STATUS)
  vi.mocked(checkUpdate).mockResolvedValue({ ...UPDATE_HAS, hasUpdate: false, latestVersion: '' })
})

describe('VersionBadge', () => {
  it('渲染版本号', async () => {
    renderBadge()
    expect(await screen.findByText('v0.6.0')).toBeInTheDocument()
  })

  it('hasUpdate=true 时叠红点', async () => {
    vi.mocked(checkUpdate).mockResolvedValue(UPDATE_HAS)
    renderBadge()
    expect(await screen.findByRole('status', { name: '有可用更新' })).toBeInTheDocument()
  })

  it('hasUpdate=false 时无红点', async () => {
    renderBadge()
    expect(await screen.findByRole('button', { name: /点击查看更新/ })).toBeInTheDocument()
    expect(screen.queryByRole('status', { name: '有可用更新' })).toBeNull()
  })

  it('check-failed 时不叠红点', async () => {
    vi.mocked(checkUpdate).mockResolvedValue({ ...UPDATE_HAS, status: 'check-failed', hasUpdate: false })
    renderBadge()
    expect(await screen.findByRole('button', { name: /点击查看更新/ })).toBeInTheDocument()
    expect(screen.queryByRole('status', { name: '有可用更新' })).toBeNull()
  })

  it('dev 构建时不叠红点（即使后端误回 hasUpdate=true）', async () => {
    vi.mocked(checkUpdate).mockResolvedValue({ ...UPDATE_HAS, isDevBuild: true, currentVersion: 'dev' })
    renderBadge()
    expect(await screen.findByRole('button', { name: /点击查看更新/ })).toBeInTheDocument()
    expect(screen.queryByRole('status', { name: '有可用更新' })).toBeNull()
  })

  it('点击版本徽章跳转到版本与更新页（ADR-0048）', async () => {
    renderBadge()
    const badge = await screen.findByRole('button', { name: /点击查看更新/ })
    await userEvent.click(badge)
    expect(navigateSpy).toHaveBeenCalledWith('/system/version')
  })
})
